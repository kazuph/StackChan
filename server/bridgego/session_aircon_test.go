package bridgego

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestHandleTurnAirconCommandSendsGeneratedIR(t *testing.T) {
	var generatePayload map[string]any
	var sentIR map[string]any
	wavBytes, err := testWAVBytes(make([]int16, OutputSampleRate/20), OutputSampleRate, 1)
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stt":
			_ = json.NewEncoder(w).Encode(map[string]any{"text": "冷房を25度にして"})
		case "/tts":
			_, _ = w.Write(wavBytes)
		case "/api/generate":
			if err := json.NewDecoder(r.Body).Decode(&generatePayload); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"raw": []int{1000, 500, 300, 200}, "frequency": 38000})
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	serverConnCh := make(chan *websocket.Conn, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		serverConnCh <- conn
	}))
	defer wsServer.Close()
	clientConn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()
	serverConn := <-serverConnCh
	defer serverConn.Close()

	cfg := LoadConfig()
	cfg.STTURL = api.URL + "/stt"
	cfg.TTSURL = api.URL + "/tts"
	cfg.IRAPIURL = api.URL
	cfg.IRStatePath = t.TempDir() + "/ir_state.json"
	cfg.AudioPacingSeconds = 0
	cfg.AudioPacingAheadPackets = 0
	cfg.TTSRetryAttempts = 1
	session := NewSession(cfg, NewClient(cfg), serverConn, log.New(io.Discard, "", 0))
	session.lastIRManufacturer = "DAIKIN"
	session.lastIRProtocol = "DAIKIN"
	session.lastAircon = DefaultAirconCommand("DAIKIN")

	var readerWG sync.WaitGroup
	readerDone := make(chan struct{})
	readerWG.Add(1)
	go func() {
		defer readerWG.Done()
		for {
			select {
			case <-readerDone:
				return
			default:
			}
			messageType, data, err := clientConn.ReadMessage()
			if err != nil {
				return
			}
			if messageType != websocket.TextMessage {
				continue
			}
			var message map[string]any
			if err := json.Unmarshal(data, &message); err != nil {
				continue
			}
			if message["type"] != "mcp" {
				continue
			}
			payload, _ := message["payload"].(map[string]any)
			params, _ := payload["params"].(map[string]any)
			if params["name"] == "self.robot.send_ir_raw" {
				args, _ := params["arguments"].(map[string]any)
				sentIR = args
			}
			_ = clientConn.WriteJSON(map[string]any{
				"type": "mcp",
				"payload": map[string]any{
					"jsonrpc": "2.0",
					"id":      payload["id"],
					"result": map[string]any{
						"content": []map[string]any{{"type": "text", "text": `{"ok":true}`}},
					},
				},
			})
		}
	}()
	readerWG.Add(1)
	go func() {
		defer readerWG.Done()
		for {
			select {
			case <-readerDone:
				return
			default:
			}
			messageType, data, err := serverConn.ReadMessage()
			if err != nil {
				return
			}
			if messageType != websocket.TextMessage {
				continue
			}
			var message map[string]any
			if err := json.Unmarshal(data, &message); err != nil {
				continue
			}
			if message["type"] == "mcp" {
				payload, _ := message["payload"].(map[string]any)
				session.HandleMCPResponse(payload)
			}
		}
	}()

	packets, err := WAVBytesToOpusPackets(wavBytes, OutputSampleRate, OutputFrameDurationMS)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := session.HandleTurn(ctx, packets); err != nil {
		t.Fatal(err)
	}
	close(readerDone)
	_ = clientConn.Close()
	_ = serverConn.Close()
	readerWG.Wait()

	if generatePayload["protocol"] != "DAIKIN" || generatePayload["mode"] != "cool" || generatePayload["temperatureC"] != float64(25) {
		t.Fatalf("generate payload=%#v", generatePayload)
	}
	if sentIR["timings_usec"] != "1000,500,300,200" || sentIR["carrier_hz"] != float64(38000) {
		t.Fatalf("sent IR=%#v", sentIR)
	}
	if session.lastAircon.Temp != 25 || session.lastAircon.Mode != "cool" {
		t.Fatalf("last aircon=%+v", session.lastAircon)
	}
}
