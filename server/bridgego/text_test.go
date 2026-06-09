package bridgego

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizeLLMTextStripsThinkTags(t *testing.T) {
	got := SanitizeLLMText("<think>internal reasoning</think>\n\nこんにちは。", "元気?")
	if got != "こんにちは。" {
		t.Fatalf("got %q", got)
	}
}

func TestSanitizeLLMTextDropsParrotReply(t *testing.T) {
	if got := SanitizeLLMText("こんにちは", "こんにちは"); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestTTSReadableTextReadsIRProtocolNamesInJapanese(t *testing.T) {
	got := TTSReadableText("メーカーはPANASONIC、プロトコルはPANASONIC_AC。DAIKINも検知したよ。")
	want := "メーカーはパナソニック、プロトコルはパナソニック エーシー。ダイキンも検知したよ。"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestTTSReadableTextSpellsUnknownAlphaNumericTokens(t *testing.T) {
	if got := TTSReadableText("ABC12を受信したよ。"); got != "エービーシーイチニを受信したよ。" {
		t.Fatalf("got %q", got)
	}
}

func TestTTSReadableTextReadsGoNaturally(t *testing.T) {
	if got := TTSReadableText("Goブリッジだよ。"); got != "ゴーブリッジだよ。" {
		t.Fatalf("got %q", got)
	}
}

func TestUsableIRLLMSpeechDropsTooShortNoise(t *testing.T) {
	if got := UsableIRLLMSpeech("ぴ"); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := UsableIRLLMSpeech("冷房を26度にしたよ。"); got != "冷房を26度にしたよ。" {
		t.Fatalf("got %q", got)
	}
}

func TestIRActionFactsAndFallbackSpeech(t *testing.T) {
	facts := IRActionFacts(map[string]any{"decoded": map[string]any{"power": true, "mode": "cool", "temperatureC": float64(26), "fan": "auto"}})
	wantFacts := []string{"運転オン", "モード=冷房", "温度=26度", "風量=自動"}
	if len(facts) != len(wantFacts) {
		t.Fatalf("got %#v", facts)
	}
	for i := range facts {
		if facts[i] != wantFacts[i] {
			t.Fatalf("got %#v want %#v", facts, wantFacts)
		}
	}
	if got := FallbackIRActionSpeech([]string{"運転オン", "モード=暖房", "温度=24度"}); got != "暖房を24度にしたよ。" {
		t.Fatalf("got %q", got)
	}
}

func TestIREffectiveManufacturer(t *testing.T) {
	if got := IREffectiveManufacturer(map[string]any{"manufacturer": "Unknown", "protocol": "PANASONIC_AC"}); got != "PANASONIC" {
		t.Fatalf("got %q", got)
	}
	if got := IREffectiveManufacturer(map[string]any{"manufacturer": "MULTIBRACKETS", "protocol": "MULTIBRACKETS"}); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestBuildIRDecodeSpeechSkipsManufacturerChangeWithoutActionFacts(t *testing.T) {
	cfg := LoadConfig()
	cfg.IRStatePath = filepath.Join(t.TempDir(), "state.json")
	session := NewSession(cfg, NewClient(cfg), nil, log.New(io.Discard, "", 0))

	reason, text := session.BuildIRDecodeSpeech(context.Background(), map[string]any{
		"manufacturer": "Panasonic",
		"protocol":     "PANASONIC_AC",
		"decoded":      map[string]any{},
	})
	if reason != "silent" || text != "" {
		t.Fatalf("reason=%q text=%q", reason, text)
	}
	if _, err := os.Stat(cfg.IRStatePath); !os.IsNotExist(err) {
		t.Fatalf("state file should not be created for empty decode: err=%v", err)
	}
}

func TestBuildIRDecodeSpeechAnnouncesMatchedManufacturerWithoutActionFacts(t *testing.T) {
	cfg := LoadConfig()
	cfg.IRStatePath = filepath.Join(t.TempDir(), "state.json")
	session := NewSession(cfg, NewClient(cfg), nil, log.New(io.Discard, "", 0))

	reason, text := session.BuildIRDecodeSpeech(context.Background(), map[string]any{
		"ok":           true,
		"manufacturer": "DAIKIN",
		"protocol":     "DAIKIN",
		"decoded":      map[string]any{},
	})
	if reason != "manufacturer_changed" || text != "メーカーがDAIKINに切り替わったよ。" {
		t.Fatalf("reason=%q text=%q", reason, text)
	}
}

func TestBuildIRDecodeSpeechPersistsManufacturerAfterActionFacts(t *testing.T) {
	cfg := LoadConfig()
	cfg.IRStatePath = filepath.Join(t.TempDir(), "state.json")
	session := NewSession(cfg, NewClient(cfg), nil, log.New(io.Discard, "", 0))

	reason, text := session.BuildIRDecodeSpeech(context.Background(), map[string]any{
		"manufacturer": "Panasonic",
		"protocol":     "PANASONIC_AC",
		"decoded":      map[string]any{"power": true, "mode": "cool", "temperatureC": float64(26)},
	})
	if reason != "manufacturer_changed" || text != "メーカーがPanasonicに切り替わったよ。" {
		t.Fatalf("reason=%q text=%q", reason, text)
	}
	data, err := os.ReadFile(cfg.IRStatePath)
	if err != nil {
		t.Fatal(err)
	}
	var state irStateFile
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if state.LastIRManufacturer != "Panasonic" || state.LastIRProtocol != "PANASONIC_AC" {
		t.Fatalf("state=%+v", state)
	}

	next := NewSession(cfg, NewClient(cfg), nil, log.New(io.Discard, "", 0))
	if next.lastIRManufacturer != "Panasonic" {
		t.Fatalf("loaded manufacturer %q", next.lastIRManufacturer)
	}
}

func TestIsRepetitiveAnswer(t *testing.T) {
	history := []Message{
		{Role: "user", Content: "君の名前は?"},
		{Role: "assistant", Content: "スタックちゃんだよ。"},
	}
	if !IsRepetitiveAnswer(history, "1足す1は?", "スタックちゃんだよ。") {
		t.Fatal("expected repetitive answer")
	}
	if IsRepetitiveAnswer(history, "君の名前は?", "スタックちゃんだよ。") {
		t.Fatal("same user question should be allowed")
	}
}

func TestLoadConfigDoesNotOverrideExistingEnvironment(t *testing.T) {
	t.Setenv("STACKCHAN_LLM_MODEL", "preset-model")
	t.Setenv("STACKCHAN_BRIDGE_HOST", "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("export STACKCHAN_BRIDGE_HOST=10.0.0.8\nSTACKCHAN_LLM_MODEL=test-model\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := LoadConfig(dir)
	if cfg.BridgeHost != "10.0.0.8" {
		t.Fatalf("bridge host got %q", cfg.BridgeHost)
	}
	if cfg.LLMModel != "preset-model" {
		t.Fatalf("llm model got %q", cfg.LLMModel)
	}
}

func TestOpusWAVRoundTrip(t *testing.T) {
	pcm := make([]int16, OutputSampleRate/2)
	for i := range pcm {
		pcm[i] = int16((i % 200) - 100)
	}
	wavBytes, err := testWAVBytes(pcm, OutputSampleRate, 1)
	if err != nil {
		t.Fatal(err)
	}
	packets, err := WAVBytesToOpusPackets(wavBytes, OutputSampleRate, OutputFrameDurationMS)
	if err != nil {
		t.Fatal(err)
	}
	if len(packets) == 0 {
		t.Fatal("expected opus packets")
	}
	decoded, err := OpusPacketsToWAVBytes(packets, InputSampleRate)
	if err != nil {
		t.Fatal(err)
	}
	sampleRate, channels, dataSize, err := inspectWAV(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if sampleRate != InputSampleRate || channels != 1 || dataSize == 0 {
		t.Fatalf("sampleRate=%d channels=%d dataSize=%d", sampleRate, channels, dataSize)
	}
}

func testWAVBytes(pcm []int16, sampleRate int, channels int) ([]byte, error) {
	dataSize := len(pcm) * 2
	var buf bytes.Buffer
	buf.WriteString("RIFF")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(36+dataSize))
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(16))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(channels))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(sampleRate*channels*2))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(channels*2))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(16))
	buf.WriteString("data")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(dataSize))
	for _, sample := range pcm {
		_ = binary.Write(&buf, binary.LittleEndian, sample)
	}
	return buf.Bytes(), nil
}

func inspectWAV(wavBytes []byte) (int, int, int, error) {
	if string(wavBytes[:4]) != "RIFF" || string(wavBytes[8:12]) != "WAVE" {
		return 0, 0, 0, os.ErrInvalid
	}
	offset := 12
	var sampleRate int
	var channels int
	var dataSize int
	for offset+8 <= len(wavBytes) {
		chunkID := string(wavBytes[offset : offset+4])
		chunkSize := int(binary.LittleEndian.Uint32(wavBytes[offset+4 : offset+8]))
		offset += 8
		if offset+chunkSize > len(wavBytes) {
			if chunkID == "data" && offset < len(wavBytes) {
				dataSize = len(wavBytes) - offset
			}
			break
		}
		switch chunkID {
		case "fmt ":
			channels = int(binary.LittleEndian.Uint16(wavBytes[offset+2 : offset+4]))
			sampleRate = int(binary.LittleEndian.Uint32(wavBytes[offset+4 : offset+8]))
		case "data":
			dataSize = chunkSize
			if dataSize == 0 && offset < len(wavBytes) {
				dataSize = len(wavBytes) - offset
			}
		}
		offset += chunkSize
		if chunkSize%2 == 1 {
			offset++
		}
	}
	if dataSize == 0 {
		if dataOffset := bytes.Index(wavBytes, []byte("data")); dataOffset >= 0 && dataOffset+8 < len(wavBytes) {
			dataSize = len(wavBytes) - dataOffset - 8
		}
	}
	return sampleRate, channels, dataSize, nil
}
