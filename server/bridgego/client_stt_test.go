package bridgego

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRunSTTUsesGateOnlyForAmbientListening(t *testing.T) {
	tests := []struct {
		name        string
		ambientGate bool
		wantGate    string
	}{
		{name: "ambient", ambientGate: true, wantGate: "stackchan"},
		{name: "conversation", ambientGate: false, wantGate: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Fatal(err)
				}
				if got := r.FormValue("language"); got != "ja" {
					t.Fatalf("language=%q", got)
				}
				if got := r.FormValue("gate"); got != tt.wantGate {
					t.Fatalf("gate=%q want=%q", got, tt.wantGate)
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"text": " テレビをつけて "})
			}))
			defer server.Close()

			cfg := LoadConfig()
			cfg.STTURL = server.URL
			text, err := NewClient(cfg).RunSTT(context.Background(), []byte("wav"), tt.ambientGate)
			if err != nil {
				t.Fatal(err)
			}
			if text != "テレビをつけて" {
				t.Fatalf("text=%q", text)
			}
		})
	}
}
