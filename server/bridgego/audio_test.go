package bridgego

import (
	"bytes"
	"context"
	"encoding/binary"
	"os/exec"
	"testing"
)

func TestWAVBytesToOpusPacketsReturnsAudioPackets(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is required for bridge audio conversion")
	}
	wav := testSilenceWAV(OutputSampleRate, OutputSampleRate/5)
	packets, err := WAVBytesToOpusPackets(wav, OutputSampleRate, OutputFrameDurationMS)
	if err != nil {
		t.Fatal(err)
	}
	if len(packets) == 0 {
		t.Fatal("expected opus packets")
	}
	if bytes.HasPrefix(packets[0], []byte("OpusHead")) {
		t.Fatal("header packet leaked into audio packets")
	}
}

func TestOpusPacketsToWAVBytesDecodesGeneratedPackets(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is required for bridge audio conversion")
	}
	packets, err := WAVBytesToOpusPackets(testSilenceWAV(OutputSampleRate, OutputSampleRate/5), OutputSampleRate, OutputFrameDurationMS)
	if err != nil {
		t.Fatal(err)
	}
	wav, err := OpusPacketsToWAVBytes(context.Background(), packets, InputSampleRate)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(wav, []byte("RIFF")) {
		t.Fatalf("decoded output is not wav: %q", wav[:4])
	}
}

func TestOpusPacketsToWAVBytesHonorsCanceledContext(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is required for bridge audio conversion")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := OpusPacketsToWAVBytes(ctx, [][]byte{{0xf8, 0xff, 0xfe}}, InputSampleRate); err == nil {
		t.Fatal("expected canceled audio conversion to fail")
	}
}

func testSilenceWAV(sampleRate int, frames int) []byte {
	dataSize := frames * 2
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+dataSize))
	b.WriteString("WAVEfmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))
	_ = binary.Write(&b, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(&b, binary.LittleEndian, uint32(sampleRate*2))
	_ = binary.Write(&b, binary.LittleEndian, uint16(2))
	_ = binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(dataSize))
	b.Write(make([]byte, dataSize))
	return b.Bytes()
}
