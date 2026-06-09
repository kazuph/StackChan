package bridgego

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestRunLLMFallsBackToGeminiWhenPrimaryFails(t *testing.T) {
	var calls []map[string]any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, payload)
		if len(calls) == 1 {
			http.Error(w, "primary down", http.StatusServiceUnavailable)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer gemini-test-key" {
			t.Fatalf("authorization got %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": "Gemini だよ。"}}}})
	}))
	defer api.Close()

	cfg := LoadConfig()
	cfg.LLMURL = api.URL
	cfg.LLMModel = "primary-model"
	cfg.GeminiFallbackURL = api.URL
	cfg.GeminiFallbackModel = "gemini-2.5-flash-lite"
	cfg.GeminiAPIKey = "gemini-test-key"
	client := NewClient(cfg)
	got, err := client.RunLLM(context.Background(), nil, "こんにちは", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "Gemini だよ。" {
		t.Fatalf("got %q", got)
	}
	if len(calls) != 2 {
		t.Fatalf("calls=%d", len(calls))
	}
	if calls[1]["model"] != "gemini-2.5-flash-lite" || calls[1]["reasoning_effort"] != "minimal" {
		t.Fatalf("fallback payload=%#v", calls[1])
	}
}

func TestRunStartupGreetingLLMSendsUserMessage(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		messages, _ := payload["messages"].([]any)
		if len(messages) != 2 {
			t.Fatalf("messages=%#v", messages)
		}
		last, _ := messages[1].(map[string]any)
		if last["role"] != "user" || !strings.Contains(last["content"].(string), "ひとこと") {
			t.Fatalf("last message=%#v", last)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": "起きたよ。"}}}})
	}))
	defer api.Close()

	cfg := LoadConfig()
	cfg.LLMURL = api.URL
	cfg.GeminiAPIKey = ""
	client := NewClient(cfg)
	got, err := client.RunStartupGreetingLLM(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "起きたよ。" {
		t.Fatalf("got %q", got)
	}
}

func TestGenerateAirconIRUsesWebAPI(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["protocol"] != "DAIKIN" || payload["mode"] != "cool" || payload["temperatureC"] != float64(25) {
			t.Fatalf("payload=%#v", payload)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"raw": []int{1000, 500, 300}, "frequency": 38000})
	}))
	defer api.Close()

	cfg := LoadConfig()
	cfg.IRAPIURL = api.URL
	client := NewClient(cfg)
	raw, frequency, err := client.GenerateAirconIR(context.Background(), AirconCommand{Protocol: "DAIKIN", Power: true, Mode: "cool", Temp: 25, Fan: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if frequency != 38000 || len(raw) != 3 || raw[0] != 1000 || raw[2] != 300 {
		t.Fatalf("raw=%v frequency=%d", raw, frequency)
	}
}

func TestRunTTSRetriesTransientGatewayErrors(t *testing.T) {
	attempts := 0
	wavBytes, err := testWAVBytes(make([]int16, OutputSampleRate), OutputSampleRate, 1)
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		_, _ = w.Write(wavBytes)
	}))
	defer api.Close()

	cfg := LoadConfig()
	cfg.TTSURL = api.URL
	cfg.TTSRetryAttempts = 2
	cfg.TTSRetryBackoffSeconds = 0
	client := NewClient(cfg)
	got, err := client.RunTTS(context.Background(), "テストだよ。")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(wavBytes) || attempts != 2 {
		t.Fatalf("bytes=%d attempts=%d", len(got), attempts)
	}
}

func TestOTAAndHealthzHandlers(t *testing.T) {
	cfg := LoadConfig()
	cfg.BridgeHost = ""
	cfg.BridgePort = 8787
	server := NewServer(cfg)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://192.168.1.23:8787/xiaozhi/ota/", nil)
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var ota map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &ota); err != nil {
		t.Fatal(err)
	}
	websocketPayload := ota["websocket"].(map[string]any)
	if websocketPayload["url"] != "ws://192.168.1.23:8787/xiaozhi/ws" {
		t.Fatalf("ota=%#v", ota)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1/healthz", nil)
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWebSocketHelloIncludesMCPFeature(t *testing.T) {
	cfg := LoadConfig()
	cfg.LLMURL = "http://127.0.0.1:1/unreachable"
	cfg.GeminiAPIKey = ""
	server := httptest.NewServer(NewServer(cfg).Handler())
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/xiaozhi/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]any{"type": "hello"}); err != nil {
		t.Fatal(err)
	}
	var hello map[string]any
	if err := conn.ReadJSON(&hello); err != nil {
		t.Fatal(err)
	}
	if hello["version"] != float64(1) || hello["transport"] != "websocket" {
		t.Fatalf("hello=%#v", hello)
	}
	features, ok := hello["features"].(map[string]any)
	if !ok || features["mcp"] != true {
		t.Fatalf("features=%#v hello=%#v", features, hello)
	}
}
