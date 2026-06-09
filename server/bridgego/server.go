package bridgego

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var (
	ErrNoActiveSession = errors.New("no active device session")
	ErrMCPTimeout      = errors.New("mcp request timed out")
)

type Server struct {
	cfg      Config
	client   *Client
	logger   *log.Logger
	upgrader websocket.Upgrader

	mu     sync.Mutex
	active *Session
}

func NewServer(cfg Config) *Server {
	logger := log.New(os.Stdout, "stackchan-voice-bridge ", log.LstdFlags)
	if cfg.EventLogPath != "" {
		if file, err := os.OpenFile(cfg.EventLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			logger = log.New(file, "", log.LstdFlags)
		}
	}
	return &Server{
		cfg:    cfg,
		client: NewClient(cfg),
		logger: logger,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(_ *http.Request) bool { return true },
		},
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/xiaozhi/ota/", s.handleOTA)
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/speak", s.handleSpeak)
	mux.HandleFunc("/aircon/command", s.handleAirconCommand)
	mux.HandleFunc("/ir/decode-speech", s.handleIRDecodeSpeech)
	mux.HandleFunc("/mcp/list", s.handleMCPList)
	mux.HandleFunc("/mcp/call", s.handleMCPCall)
	mux.HandleFunc("/xiaozhi/ws", s.handleWS)
	return mux
}

func (s *Server) Addr() string {
	return fmt.Sprintf("0.0.0.0:%d", s.cfg.BridgePort)
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	server := &http.Server{
		Addr:              s.Addr(),
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		s.logger.Printf("listening addr=%s", server.Addr)
		errCh <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) ActiveSession() *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil || s.active.isClosed() {
		return nil
	}
	return s.active
}

func (s *Server) setActive(session *Session) {
	s.mu.Lock()
	s.active = session
	s.mu.Unlock()
}

func (s *Server) clearActive(session *Session) {
	s.mu.Lock()
	if s.active == session {
		s.active = nil
	}
	s.mu.Unlock()
}

func (s *Server) handleOTA(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	bridgeHost := s.resolveBridgeHost(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"websocket": map[string]any{
			"url":     fmt.Sprintf("ws://%s:%d/xiaozhi/ws", bridgeHost, s.cfg.BridgePort),
			"token":   "",
			"version": 1,
		},
		"server_time": map[string]any{
			"timestamp":       time.Now().UnixMilli(),
			"timezone_offset": s.cfg.TimezoneOffsetMinutes,
		},
	})
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var bridgeHost any
	if s.cfg.BridgeHost != "" {
		bridgeHost = s.cfg.BridgeHost
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "ok",
		"bridge_host": bridgeHost,
		"bridge_port": s.cfg.BridgePort,
		"stt_url":     s.cfg.STTURL,
		"tts_url":     s.cfg.TTSURL,
		"llm_url":     s.cfg.LLMURL,
	})
}

func (s *Server) handleSpeak(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	payload, err := readJSONObject(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	text := strings.TrimSpace(toString(payload["text"]))
	if text == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	session := s.ActiveSession()
	if session == nil {
		writeError(w, http.StatusConflict, ErrNoActiveSession.Error())
		return
	}
	session.TriggerManualSpeech(text)
	writeJSON(w, http.StatusOK, map[string]any{"status": "queued", "session_id": session.ID(), "text": text})
}

func (s *Server) handleAirconCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	payload, err := readJSONObject(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	text := strings.TrimSpace(toString(payload["text"]))
	if text == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	session := s.ActiveSession()
	if session == nil {
		writeError(w, http.StatusConflict, ErrNoActiveSession.Error())
		return
	}
	handled, answer := session.HandleAirconCommand(r.Context(), text)
	if !handled {
		writeError(w, http.StatusBadRequest, "not an aircon command")
		return
	}
	session.TriggerManualSpeech(answer)
	writeJSON(w, http.StatusOK, map[string]any{"status": "sent", "session_id": session.ID(), "text": text, "answer": answer})
}

func (s *Server) handleIRDecodeSpeech(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	payload, err := readJSONObject(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	session := s.ActiveSession()
	if session == nil {
		writeError(w, http.StatusConflict, ErrNoActiveSession.Error())
		return
	}
	session.TriggerIRDecodeSpeech(payload)
	writeJSON(w, http.StatusOK, map[string]any{"status": "queued", "session_id": session.ID()})
}

func (s *Server) handleMCPList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	payload := readOptionalObject(r)
	session := s.ActiveSession()
	if session == nil {
		writeError(w, http.StatusConflict, ErrNoActiveSession.Error())
		return
	}
	params := map[string]any{}
	if cursor := strings.TrimSpace(toString(payload["cursor"])); cursor != "" {
		params["cursor"] = cursor
	}
	resp, err := session.CallMCP(map[string]any{"method": "tools/list", "params": params}, timeoutFromPayload(payload))
	if err != nil {
		writeSessionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleMCPCall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	payload, err := readJSONObject(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	name := strings.TrimSpace(toString(payload["name"]))
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	arguments, ok := payload["arguments"].(map[string]any)
	if !ok {
		if payload["arguments"] == nil {
			arguments = map[string]any{}
		} else {
			writeError(w, http.StatusBadRequest, "arguments must be an object")
			return
		}
	}
	session := s.ActiveSession()
	if session == nil {
		writeError(w, http.StatusConflict, ErrNoActiveSession.Error())
		return
	}
	resp, err := session.CallMCP(map[string]any{
		"method": "tools/call",
		"params": map[string]any{"name": name, "arguments": arguments},
	}, timeoutFromPayload(payload))
	if err != nil {
		writeSessionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Printf("websocket_upgrade_failed: %v", err)
		return
	}
	session := NewSession(s.cfg, s.client, conn, s.logger)
	s.setActive(session)
	defer func() {
		session.Close()
		s.clearActive(session)
		_ = conn.Close()
		s.logger.Printf("session=%s closed", session.ID())
	}()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	mt, data, err := conn.ReadMessage()
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		s.logger.Printf("session=%s hello_timeout_or_read_failed: %v", session.ID(), err)
		return
	}
	if mt != websocket.TextMessage {
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseUnsupportedData, ""), time.Now().Add(time.Second))
		return
	}
	hello, err := ParseJSONMap(data)
	if err != nil || hello["type"] != "hello" {
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseUnsupportedData, ""), time.Now().Add(time.Second))
		return
	}
	s.logger.Printf("session=%s connected hello=%v", session.ID(), hello)
	if err := session.SendJSON(map[string]any{
		"type":       "hello",
		"version":    1,
		"features":   map[string]any{"mcp": true},
		"transport":  "websocket",
		"session_id": session.ID(),
		"audio_params": map[string]any{
			"format":         "opus",
			"sample_rate":    OutputSampleRate,
			"channels":       1,
			"frame_duration": OutputFrameDurationMS,
		},
	}); err != nil {
		return
	}
	session.TriggerStartupGreeting()

	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if mt == websocket.BinaryMessage {
			session.AppendAudioPacket(data)
			continue
		}
		if mt != websocket.TextMessage {
			continue
		}
		payload, err := ParseJSONMap(data)
		if err != nil {
			s.logger.Printf("session=%s invalid_json payload=%q error=%v", session.ID(), string(data), err)
			continue
		}
		switch payload["type"] {
		case "listen":
			state := toString(payload["state"])
			s.logger.Printf("session=%s listen state=%s mode=%s", session.ID(), state, toString(payload["mode"]))
			if state == "start" {
				_ = session.StartListening()
			} else if state == "stop" {
				session.StopListening()
			}
		case "abort":
			_ = session.CancelResponse()
		case "mcp":
			mcpPayload, ok := payload["payload"].(map[string]any)
			if ok {
				session.HandleMCPResponse(mcpPayload)
			}
		}
	}
}

func (s *Server) resolveBridgeHost(r *http.Request) string {
	if s.cfg.BridgeHost != "" {
		return s.cfg.BridgeHost
	}
	if forwarded := strings.TrimSpace(r.Header.Get("x-forwarded-host")); forwarded != "" {
		return strings.TrimSpace(strings.Split(forwarded, ",")[0])
	}
	if host := r.Host; host != "" {
		if parsedHost, _, err := net.SplitHostPort(host); err == nil && parsedHost != "" {
			return parsedHost
		}
		return host
	}
	return "127.0.0.1"
}

func readJSONObject(r *http.Request) (map[string]any, error) {
	var payload map[string]any
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	if payload == nil {
		payload = map[string]any{}
	}
	return payload, nil
}

func readOptionalObject(r *http.Request) map[string]any {
	payload, err := readJSONObject(r)
	if err != nil {
		return map[string]any{}
	}
	return payload
}

func timeoutFromPayload(payload map[string]any) time.Duration {
	seconds := 10.0
	switch v := payload["timeout"].(type) {
	case json.Number:
		if parsed, err := strconv.ParseFloat(v.String(), 64); err == nil {
			seconds = parsed
		}
	case float64:
		seconds = v
	case string:
		if parsed, err := strconv.ParseFloat(v, 64); err == nil {
			seconds = parsed
		}
	}
	return time.Duration(seconds * float64(time.Second))
}

func writeSessionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNoActiveSession):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrMCPTimeout):
		writeError(w, http.StatusGatewayTimeout, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]any{"detail": detail})
}
