package bridgego

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestConversationSilenceRestartsAmbientListening(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"text": ""})
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
	cfg.STTURL = api.URL
	cfg.EnableTVVoiceControl = true
	session := NewSession(cfg, NewClient(cfg), serverConn, log.New(io.Discard, "", 0))
	session.conversationMode = true

	wavBytes, err := testWAVBytes(make([]int16, InputSampleRate/20), InputSampleRate, 1)
	if err != nil {
		t.Fatal(err)
	}
	packets, err := WAVBytesToOpusPackets(wavBytes, InputSampleRate, OutputFrameDurationMS)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.HandleTurn(context.Background(), packets); err != nil {
		t.Fatal(err)
	}

	if err := clientConn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var message map[string]any
	if err := clientConn.ReadJSON(&message); err != nil {
		t.Fatal(err)
	}
	if message["type"] != "system" || message["command"] != "start_listening" {
		t.Fatalf("message=%#v", message)
	}
}
