package bridgego

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type Session struct {
	cfg       Config
	client    *Client
	conn      *websocket.Conn
	logger    *log.Logger
	id        string
	history   []Message
	listening bool
	input     [][]byte

	sendMu   sync.Mutex
	stateMu  sync.Mutex
	irMu     sync.Mutex
	respMu   sync.Mutex
	respStop context.CancelFunc
	respID   int64

	mcpNextID               int
	mcpPending              map[string]chan map[string]any
	pendingEndConversation  bool
	pendingIdleAfterTTS     bool
	consecutiveNoInputCount int
	closed                  bool
	lastIRManufacturer      string
	lastIRProtocol          string
	lastAircon              AirconCommand
	conversationMode        bool
}

func NewSession(cfg Config, client *Client, conn *websocket.Conn, logger *log.Logger) *Session {
	if logger == nil {
		logger = log.Default()
	}
	session := &Session{
		cfg:        cfg,
		client:     client,
		conn:       conn,
		logger:     logger,
		id:         uuid.NewString(),
		mcpNextID:  1,
		mcpPending: map[string]chan map[string]any{},
	}
	session.lastIRManufacturer = session.loadLastIRManufacturer()
	session.loadIRState()
	return session
}

func (s *Session) ID() string {
	return s.id
}

func (s *Session) Close() {
	s.stateMu.Lock()
	s.closed = true
	s.stateMu.Unlock()
	_ = s.CancelResponse()
	if s.conn != nil {
		_ = s.conn.Close()
	}
}

func (s *Session) SendJSON(payload map[string]any) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.isClosed() {
		return nil
	}
	return s.conn.WriteJSON(payload)
}

func (s *Session) SendAudio(payload []byte) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.isClosed() {
		return nil
	}
	return s.conn.WriteMessage(websocket.BinaryMessage, payload)
}

func (s *Session) SendAudioStream(ctx context.Context, packets [][]byte) error {
	start := time.Now()
	for i, packet := range packets {
		if err := ctx.Err(); err != nil {
			return err
		}
		pacedIndex := i - s.cfg.AudioPacingAheadPackets
		if pacedIndex < 0 {
			pacedIndex = 0
		}
		target := start.Add(time.Duration(float64(pacedIndex)*s.cfg.AudioPacingSeconds) * time.Second)
		if delay := time.Until(target); delay > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
		if err := s.SendAudio(packet); err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) SpawnResponse(fn func(context.Context) error) {
	_ = s.CancelResponse()
	ctx, cancel := context.WithCancel(context.Background())
	s.respMu.Lock()
	s.respID++
	respID := s.respID
	s.respStop = cancel
	s.respMu.Unlock()
	go func() {
		defer func() {
			s.respMu.Lock()
			if s.respID == respID {
				s.respStop = nil
			}
			s.respMu.Unlock()
		}()
		if err := fn(ctx); err != nil && !errors.Is(err, context.Canceled) {
			s.logger.Printf("session=%s bridge_response_failed: %v", s.id, err)
			_ = s.SendJSON(map[string]any{"type": "alert", "status": "Bridge Error", "message": UserSafeAlertMessage(err), "emotion": "sad"})
			_ = s.SendJSON(map[string]any{"type": "tts", "state": "stop"})
		}
	}()
}

func (s *Session) CancelResponse() error {
	s.respMu.Lock()
	cancel := s.respStop
	s.respStop = nil
	s.respMu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	return s.SendJSON(map[string]any{"type": "tts", "state": "stop"})
}

func (s *Session) StartListening() error {
	if err := s.CancelResponse(); err != nil {
		return err
	}
	s.stateMu.Lock()
	s.listening = true
	s.input = nil
	s.stateMu.Unlock()
	return nil
}

func (s *Session) AppendAudioPacket(payload []byte) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.listening {
		s.input = append(s.input, append([]byte(nil), payload...))
	}
}

func (s *Session) StopListening() {
	s.stateMu.Lock()
	s.listening = false
	packets := s.input
	s.input = nil
	s.stateMu.Unlock()
	if len(packets) == 0 {
		if s.cfg.EnableTVVoiceControl {
			_ = s.StartAmbientListening()
		} else {
			s.SpawnResponse(s.handleMissedInput)
		}
		return
	}
	s.SpawnResponse(func(ctx context.Context) error {
		return s.HandleTurn(ctx, packets)
	})
}

func (s *Session) TriggerStartupGreeting() {
	s.SpawnResponse(s.SendStartupGreeting)
}

func (s *Session) TriggerManualSpeech(text string) {
	s.SpawnResponse(func(ctx context.Context) error {
		s.stateMu.Lock()
		s.pendingIdleAfterTTS = true
		s.stateMu.Unlock()
		return s.Respond(ctx, text, false)
	})
}

func (s *Session) TriggerIRDecodeSpeech(payload map[string]any) {
	go func() {
		s.irMu.Lock()
		defer s.irMu.Unlock()
		reason, text := s.BuildIRDecodeSpeech(context.Background(), payload)
		if text == "" {
			s.logger.Printf("session=%s ir_decode_speech_skipped reason=%s", s.id, reason)
			return
		}
		s.SpawnResponse(func(ctx context.Context) error {
			s.stateMu.Lock()
			s.pendingIdleAfterTTS = true
			s.stateMu.Unlock()
			return s.Respond(ctx, text, false)
		})
	}()
}

func (s *Session) BuildIRDecodeSpeech(ctx context.Context, payload map[string]any) (string, string) {
	manufacturer := IREffectiveManufacturer(payload)
	protocol := strings.TrimSpace(toString(payload["protocol"]))
	facts := IRActionFacts(payload)
	s.stateMu.Lock()
	if protocol != "" && strings.ToUpper(protocol) != "UNKNOWN" && strings.ToUpper(protocol) != "MULTIBRACKETS" {
		s.lastIRProtocol = protocol
		if decoded, ok := payload["decoded"].(map[string]any); ok && len(decoded) > 0 {
			s.lastAircon = commandFromDecoded(protocol, decoded)
		} else if s.lastAircon.Protocol == "" {
			s.lastAircon = DefaultAirconCommand(protocol)
		} else {
			s.lastAircon.Protocol = protocol
		}
	}
	if manufacturer != "" && manufacturer != s.lastIRManufacturer {
		if len(facts) == 0 && !IRPayloadMatchedAC(payload) {
			s.stateMu.Unlock()
			return "silent", ""
		}
		s.lastIRManufacturer = manufacturer
		s.saveIRStateLocked()
		s.stateMu.Unlock()
		return "manufacturer_changed", "メーカーが" + manufacturer + "に切り替わったよ。"
	}
	if protocol != "" {
		s.saveIRStateLocked()
	}
	s.stateMu.Unlock()
	if len(facts) == 0 {
		return "silent", ""
	}
	text, err := s.client.RunIREventLLM(ctx, facts)
	if err != nil {
		s.logger.Printf("session=%s ir_event_llm_failed facts=%v error=%v", s.id, facts, err)
	}
	if text == "" {
		text = FallbackIRActionSpeech(facts)
	}
	return "action", text
}

type irStateFile struct {
	LastIRManufacturer string `json:"last_ir_manufacturer"`
	LastIRProtocol     string `json:"last_ir_protocol"`
	Power              bool   `json:"power"`
	Mode               string `json:"mode"`
	Temp               int    `json:"temp"`
	Fan                string `json:"fan"`
}

func (s *Session) loadLastIRManufacturer() string {
	path := strings.TrimSpace(s.cfg.IRStatePath)
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var state irStateFile
	if err := json.Unmarshal(data, &state); err != nil {
		s.logger.Printf("session=%s ir_state_load_failed path=%s error=%v", s.id, path, err)
		return ""
	}
	return strings.TrimSpace(state.LastIRManufacturer)
}

func (s *Session) loadIRState() {
	path := strings.TrimSpace(s.cfg.IRStatePath)
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var state irStateFile
	if err := json.Unmarshal(data, &state); err != nil {
		s.logger.Printf("session=%s ir_state_load_failed path=%s error=%v", s.id, path, err)
		return
	}
	s.lastIRManufacturer = strings.TrimSpace(state.LastIRManufacturer)
	s.lastIRProtocol = strings.TrimSpace(state.LastIRProtocol)
	if s.lastIRProtocol != "" {
		s.lastAircon = AirconCommand{
			Protocol: s.lastIRProtocol,
			Power:    state.Power,
			Mode:     state.Mode,
			Temp:     state.Temp,
			Fan:      state.Fan,
		}
	}
}

func (s *Session) saveLastIRManufacturerLocked(manufacturer string) {
	path := strings.TrimSpace(s.cfg.IRStatePath)
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		s.logger.Printf("session=%s ir_state_dir_failed path=%s error=%v", s.id, path, err)
		return
	}
	data, err := json.Marshal(irStateFile{LastIRManufacturer: manufacturer, LastIRProtocol: s.lastIRProtocol, Power: s.lastAircon.Power, Mode: s.lastAircon.Mode, Temp: s.lastAircon.Temp, Fan: s.lastAircon.Fan})
	if err != nil {
		s.logger.Printf("session=%s ir_state_encode_failed error=%v", s.id, err)
		return
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		s.logger.Printf("session=%s ir_state_save_failed path=%s error=%v", s.id, path, err)
	}
}

func (s *Session) saveIRStateLocked() {
	s.saveLastIRManufacturerLocked(s.lastIRManufacturer)
}

func (s *Session) HandleTurn(ctx context.Context, packets [][]byte) error {
	recognitionCtx, cancelRecognition := context.WithTimeout(ctx, bridgeRequestTimeout)
	defer cancelRecognition()
	restartAmbient := s.cfg.EnableTVVoiceControl
	if s.cfg.EnableTVVoiceControl {
		defer func() {
			if !restartAmbient {
				return
			}
			s.stateMu.Lock()
			conversationMode := s.conversationMode
			s.stateMu.Unlock()
			if conversationMode {
				return
			}
			if err := s.StartAmbientListening(); err != nil {
				s.logger.Printf("session=%s ambient_listening_restart_failed: %v", s.id, err)
			}
		}()
	}
	wavBytes, err := OpusPacketsToWAVBytes(recognitionCtx, packets, InputSampleRate)
	if err != nil {
		return err
	}
	s.stateMu.Lock()
	conversationMode := s.conversationMode
	s.stateMu.Unlock()
	ambientGate := s.cfg.EnableTVVoiceControl && !conversationMode
	userText, err := s.client.RunSTT(recognitionCtx, wavBytes, ambientGate)
	if err != nil {
		return err
	}
	if userText == "" {
		if s.cfg.EnableTVVoiceControl {
			if conversationMode {
				return s.StartAmbientListening()
			}
			return nil
		}
		return s.handleMissedInput(ctx)
	}
	s.stateMu.Lock()
	s.consecutiveNoInputCount = 0
	history := append([]Message(nil), s.history...)
	conversationMode = s.conversationMode
	s.stateMu.Unlock()

	if s.cfg.EnableTVVoiceControl && !conversationMode {
		handled, err := s.HandleLGTVVoiceCommand(ctx, userText)
		if handled {
			if err != nil {
				return err
			}
			if err := s.StartAmbientListening(); err != nil {
				return err
			}
			restartAmbient = false
			return s.SendJSON(TVCommandAcknowledgement(userText))
		}
		remainder, wakeDetected := StripStackChanWakePrefix(userText)
		if wakeDetected {
			s.stateMu.Lock()
			s.conversationMode = true
			s.stateMu.Unlock()
			if remainder != "" {
				userText = remainder
			} else {
				if err := s.SendJSON(map[string]any{"type": "stt", "text": "聞いてるよ"}); err != nil {
					return err
				}
				return s.Respond(ctx, "なあに？", true)
			}
		} else {
			s.logger.Printf("session=%s ambient_voice_ignored text=%q", s.id, userText)
			return nil
		}
	}
	if err := s.SendJSON(map[string]any{"type": "stt", "text": SanitizeDisplayTranscript(userText)}); err != nil {
		return err
	}
	if handled, answer := s.HandleAirconCommand(ctx, userText); handled {
		s.stateMu.Lock()
		s.history = append(s.history, Message{Role: "user", Content: userText}, Message{Role: "assistant", Content: answer})
		s.pendingIdleAfterTTS = true
		s.stateMu.Unlock()
		return s.Respond(ctx, answer, true)
	}
	end, err := s.client.ShouldEndConversation(ctx, history, userText)
	if err != nil {
		s.logger.Printf("session=%s end_conversation_check_failed user_text=%q error=%v", s.id, userText, err)
		end = false
	}
	s.stateMu.Lock()
	s.pendingEndConversation = end
	if end && s.cfg.EnableTVVoiceControl {
		s.conversationMode = false
	}
	s.stateMu.Unlock()
	rawAnswer, err := s.client.RunLLM(ctx, history, userText, "")
	answer := ""
	if err != nil {
		s.logger.Printf("session=%s llm_failed error=%v", s.id, err)
	} else {
		answer = SanitizeLLMText(rawAnswer, userText)
		if IsRepetitiveAnswer(history, userText, answer) {
			rawAnswer, err = s.client.RunLLM(ctx, history, userText, RepetitionRetryPrompt)
			if err == nil {
				answer = SanitizeLLMText(rawAnswer, userText)
			}
		}
	}
	if answer == "" {
		if end {
			answer = "おやすみなさい。またね。"
		} else {
			answer = "ごめんね。今ちょっと考え中だよ。"
		}
	}
	s.stateMu.Lock()
	s.history = append(s.history, Message{Role: "user", Content: userText}, Message{Role: "assistant", Content: answer})
	s.stateMu.Unlock()
	return s.Respond(ctx, answer, true)
}

func (s *Session) SendStartupGreeting(ctx context.Context) error {
	raw, err := s.client.RunStartupGreetingLLM(ctx)
	if err != nil {
		s.logger.Printf("session=%s startup_llm_failed error=%v", s.id, err)
		raw = ""
	}
	text := UsableIRLLMSpeech(raw)
	if text == "" {
		text = "ふふ、起きたよ。"
	}
	s.stateMu.Lock()
	s.pendingIdleAfterTTS = true
	s.stateMu.Unlock()
	return s.Respond(ctx, text, true)
}

func (s *Session) Respond(ctx context.Context, text string, fromSTT bool) error {
	if strings.TrimSpace(text) == "" {
		text = "すみません。うまく答えを作れませんでした。"
	}
	speechText := TTSReadableText(text)
	s.logger.Printf("session=%s respond=%q", s.id, text)
	if speechText != text {
		s.logger.Printf("session=%s respond_tts=%q", s.id, speechText)
	}
	if !fromSTT {
		if err := s.SendJSON(map[string]any{"type": "stt", "text": ""}); err != nil {
			return err
		}
	}
	for _, payload := range []map[string]any{
		{"type": "llm", "emotion": "neutral"},
		{"type": "tts", "state": "start"},
		{"type": "tts", "state": "sentence_start", "text": text},
	} {
		if err := s.SendJSON(payload); err != nil {
			return err
		}
	}
	wavBytes, err := s.client.RunTTS(ctx, speechText)
	if err != nil {
		return err
	}
	packets, err := WAVBytesToOpusPackets(wavBytes, OutputSampleRate, OutputFrameDurationMS)
	if err != nil {
		return err
	}
	s.logger.Printf(
		"session=%s tts_packets=%d approx_duration_sec=%.2f pacing_sec=%.3f ahead_packets=%d",
		s.id,
		len(packets),
		float64(len(packets))*s.cfg.AudioPacingSeconds,
		s.cfg.AudioPacingSeconds,
		s.cfg.AudioPacingAheadPackets,
	)
	if err := s.SendAudioStream(ctx, packets); err != nil {
		return err
	}
	s.stateMu.Lock()
	end := s.pendingEndConversation
	idle := s.pendingIdleAfterTTS
	if end {
		s.pendingEndConversation = false
	} else if idle {
		s.pendingIdleAfterTTS = false
	}
	s.stateMu.Unlock()
	if end {
		if err := s.SendJSON(map[string]any{"type": "system", "command": "end_conversation"}); err != nil {
			return err
		}
	} else if idle {
		if err := s.SendJSON(map[string]any{"type": "system", "command": "idle_after_tts"}); err != nil {
			return err
		}
	}
	return s.SendJSON(map[string]any{"type": "tts", "state": "stop"})
}

func (s *Session) handleMissedInput(ctx context.Context) error {
	s.stateMu.Lock()
	s.consecutiveNoInputCount++
	shouldStop := s.consecutiveNoInputCount >= s.cfg.MaxConsecutiveNoInputs
	if shouldStop {
		s.pendingIdleAfterTTS = true
	}
	s.stateMu.Unlock()
	if shouldStop {
		return s.Respond(ctx, "聞こえませんでした。いったん終わるね。", false)
	}
	return s.Respond(ctx, "聞こえませんでした。もう一度お願いします。", false)
}

func (s *Session) CallMCP(request map[string]any, timeout time.Duration) (map[string]any, error) {
	if s.isClosed() {
		return nil, ErrNoActiveSession
	}
	requestID, ok := request["id"]
	if !ok || requestID == nil {
		requestID = s.mcpNextID
		s.mcpNextID++
		if s.mcpNextID >= 2_000_000_000 {
			s.mcpNextID = 1
		}
	}
	request["id"] = requestID
	if _, ok := request["jsonrpc"]; !ok {
		request["jsonrpc"] = "2.0"
	}
	key := toString(requestID)
	ch := make(chan map[string]any, 1)
	s.stateMu.Lock()
	s.mcpPending[key] = ch
	s.stateMu.Unlock()
	defer func() {
		s.stateMu.Lock()
		delete(s.mcpPending, key)
		s.stateMu.Unlock()
	}()
	if err := s.SendJSON(map[string]any{"type": "mcp", "payload": request}); err != nil {
		return nil, err
	}
	select {
	case resp := <-ch:
		return resp, nil
	case <-time.After(timeout):
		return nil, ErrMCPTimeout
	}
}

func (s *Session) HandleMCPResponse(payload map[string]any) {
	key := toString(payload["id"])
	if key == "" {
		s.logger.Printf("session=%s mcp_notification payload=%v", s.id, payload)
		return
	}
	s.stateMu.Lock()
	ch := s.mcpPending[key]
	s.stateMu.Unlock()
	if ch == nil {
		s.logger.Printf("session=%s mcp_unmatched_response id=%s payload=%v", s.id, key, payload)
		return
	}
	ch <- payload
}

func (s *Session) isClosed() bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.closed
}

func ParseJSONMap(data []byte) (map[string]any, error) {
	var payload map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	err := dec.Decode(&payload)
	return payload, err
}
