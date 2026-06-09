package bridgego

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os/exec"
)

func PCMToWAVBytes(pcm []int16, sampleRate int, channels int) ([]byte, error) {
	if channels <= 0 {
		return nil, errors.New("channels must be positive")
	}
	dataSize := len(pcm) * 2
	var out bytes.Buffer
	out.WriteString("RIFF")
	_ = binary.Write(&out, binary.LittleEndian, uint32(36+dataSize))
	out.WriteString("WAVEfmt ")
	_ = binary.Write(&out, binary.LittleEndian, uint32(16))
	_ = binary.Write(&out, binary.LittleEndian, uint16(1))
	_ = binary.Write(&out, binary.LittleEndian, uint16(channels))
	_ = binary.Write(&out, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(&out, binary.LittleEndian, uint32(sampleRate*channels*2))
	_ = binary.Write(&out, binary.LittleEndian, uint16(channels*2))
	_ = binary.Write(&out, binary.LittleEndian, uint16(16))
	out.WriteString("data")
	_ = binary.Write(&out, binary.LittleEndian, uint32(dataSize))
	for _, sample := range pcm {
		_ = binary.Write(&out, binary.LittleEndian, sample)
	}
	return out.Bytes(), nil
}

func WAVBytesToPCM(wavBytes []byte) ([]int16, int, int, error) {
	reader := bytes.NewReader(wavBytes)
	header := make([]byte, 12)
	if _, err := io.ReadFull(reader, header); err != nil {
		return nil, 0, 0, err
	}
	if string(header[:4]) != "RIFF" || string(header[8:12]) != "WAVE" {
		return nil, 0, 0, errors.New("invalid wav header")
	}
	var sampleRate int
	var channels int
	var bitsPerSample int
	var pcm []int16
	for reader.Len() > 0 {
		chunkHeader := make([]byte, 8)
		if _, err := io.ReadFull(reader, chunkHeader); err != nil {
			return nil, 0, 0, err
		}
		chunkID := string(chunkHeader[:4])
		chunkSize := int(binary.LittleEndian.Uint32(chunkHeader[4:8]))
		if chunkID == "data" && chunkSize == 0 {
			chunkSize = reader.Len()
		}
		if chunkSize < 0 || chunkSize > reader.Len() {
			return nil, 0, 0, errors.New("invalid wav chunk size")
		}
		chunk := make([]byte, chunkSize)
		if _, err := io.ReadFull(reader, chunk); err != nil {
			return nil, 0, 0, err
		}
		if chunkSize%2 == 1 && reader.Len() > 0 {
			_, _ = reader.ReadByte()
		}
		switch chunkID {
		case "fmt ":
			if len(chunk) < 16 {
				return nil, 0, 0, errors.New("short wav fmt chunk")
			}
			audioFormat := binary.LittleEndian.Uint16(chunk[0:2])
			if audioFormat != 1 {
				return nil, 0, 0, errors.New("only pcm wav is supported")
			}
			channels = int(binary.LittleEndian.Uint16(chunk[2:4]))
			sampleRate = int(binary.LittleEndian.Uint32(chunk[4:8]))
			bitsPerSample = int(binary.LittleEndian.Uint16(chunk[14:16]))
			if bitsPerSample != 16 {
				return nil, 0, 0, errors.New("only 16-bit wav is supported")
			}
		case "data":
			if len(chunk)%2 != 0 {
				return nil, 0, 0, errors.New("odd pcm data size")
			}
			pcm = make([]int16, len(chunk)/2)
			for i := range pcm {
				pcm[i] = int16(binary.LittleEndian.Uint16(chunk[i*2 : i*2+2]))
			}
		}
	}
	if sampleRate == 0 || channels == 0 || bitsPerSample == 0 {
		return nil, 0, 0, errors.New("missing wav fmt chunk")
	}
	return pcm, sampleRate, channels, nil
}

func OpusPacketsToWAVBytes(packets [][]byte, sampleRate int) ([]byte, error) {
	if len(packets) == 0 {
		return nil, errors.New("no opus packets")
	}
	ogg := BuildOggOpus(packets)
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "ogg", "-i", "pipe:0", "-ac", "1", "-ar", fmt.Sprint(sampleRate), "-f", "wav", "pipe:1")
	cmd.Stdin = bytes.NewReader(ogg)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg opus decode failed: %w: %s", err, stderr.String())
	}
	return out, nil
}

func WAVBytesToOpusPackets(wavBytes []byte, sampleRate int, frameDurationMS int) ([][]byte, error) {
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-i", "pipe:0", "-ac", "1", "-ar", fmt.Sprint(sampleRate), "-c:a", "libopus", "-b:a", "32k", "-application", "voip", "-frame_duration", fmt.Sprint(frameDurationMS), "-vbr", "off", "-f", "ogg", "pipe:1")
	cmd.Stdin = bytes.NewReader(wavBytes)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg opus encode failed: %w: %s", err, stderr.String())
	}
	return ParseOggOpusPackets(out)
}

func BuildOggOpus(audioPackets [][]byte) []byte {
	var out bytes.Buffer
	serial := uint32(0x53544348)
	seq := uint32(0)
	writeOggPage(&out, serial, seq, 0x02, 0, [][]byte{opusHeadPacket()})
	seq++
	writeOggPage(&out, serial, seq, 0, 0, [][]byte{[]byte("OpusTags\x08\x00\x00\x00StackChan\x00\x00\x00\x00")})
	seq++
	granule := uint64(0)
	for _, packet := range audioPackets {
		granule += uint64(opusPacketSamples(packet))
		writeOggPage(&out, serial, seq, 0, granule, [][]byte{packet})
		seq++
	}
	writeOggPage(&out, serial, seq, 0x04, granule, nil)
	return out.Bytes()
}

func ParseOggOpusPackets(data []byte) ([][]byte, error) {
	reader := bytes.NewReader(data)
	var packets [][]byte
	var pending bytes.Buffer
	headerPackets := 0
	for {
		pagePackets, continued, err := readOggPage(reader)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if continued && pending.Len() == 0 {
			return nil, errors.New("continued ogg packet without previous data")
		}
		for _, part := range pagePackets {
			if continued || pending.Len() > 0 {
				pending.Write(part)
				if len(part) > 0 {
					continued = false
					part = append([]byte(nil), pending.Bytes()...)
					pending.Reset()
				} else {
					continue
				}
			}
			headerPackets++
			if headerPackets <= 2 {
				continue
			}
			if len(part) > 0 {
				packets = append(packets, part)
			}
		}
	}
	return packets, nil
}

func opusHeadPacket() []byte {
	packet := make([]byte, 19)
	copy(packet, "OpusHead")
	packet[8] = 1
	packet[9] = 1
	binary.LittleEndian.PutUint16(packet[10:12], 0)
	binary.LittleEndian.PutUint32(packet[12:16], 48000)
	return packet
}

func writeOggPage(out *bytes.Buffer, serial, seq uint32, headerType byte, granule uint64, packets [][]byte) {
	var body bytes.Buffer
	var lacing []byte
	for _, packet := range packets {
		body.Write(packet)
		for remaining := len(packet); remaining >= 255; remaining -= 255 {
			lacing = append(lacing, 255)
			if remaining == 255 {
				lacing = append(lacing, 0)
			}
		}
		if len(packet)%255 != 0 {
			lacing = append(lacing, byte(len(packet)%255))
		}
	}
	header := make([]byte, 27+len(lacing))
	copy(header, "OggS")
	header[5] = headerType
	binary.LittleEndian.PutUint64(header[6:14], granule)
	binary.LittleEndian.PutUint32(header[14:18], serial)
	binary.LittleEndian.PutUint32(header[18:22], seq)
	header[26] = byte(len(lacing))
	copy(header[27:], lacing)
	page := append(header, body.Bytes()...)
	binary.LittleEndian.PutUint32(page[22:26], oggCRC(page))
	out.Write(page)
}

func readOggPage(reader *bytes.Reader) ([][]byte, bool, error) {
	header := make([]byte, 27)
	if _, err := io.ReadFull(reader, header); err != nil {
		return nil, false, err
	}
	if string(header[:4]) != "OggS" {
		return nil, false, errors.New("invalid ogg capture pattern")
	}
	continued := header[5]&0x01 != 0
	segments := int(header[26])
	lacing := make([]byte, segments)
	if _, err := io.ReadFull(reader, lacing); err != nil {
		return nil, false, err
	}
	bodyLen := 0
	for _, size := range lacing {
		bodyLen += int(size)
	}
	body := make([]byte, bodyLen)
	if _, err := io.ReadFull(reader, body); err != nil {
		return nil, false, err
	}
	var packets [][]byte
	offset := 0
	start := 0
	for _, size := range lacing {
		offset += int(size)
		if size < 255 {
			packets = append(packets, append([]byte(nil), body[start:offset]...))
			start = offset
		}
	}
	if start < offset {
		packets = append(packets, append([]byte(nil), body[start:offset]...))
	}
	return packets, continued, nil
}

func oggCRC(page []byte) uint32 {
	var crc uint32
	for _, b := range page {
		crc ^= uint32(b) << 24
		for i := 0; i < 8; i++ {
			if crc&0x80000000 != 0 {
				crc = (crc << 1) ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func opusPacketSamples(packet []byte) int {
	if len(packet) == 0 {
		return 0
	}
	config := packet[0] >> 3
	frameCount := 1
	switch packet[0] & 0x03 {
	case 0:
		frameCount = 1
	case 1, 2:
		frameCount = 2
	case 3:
		if len(packet) > 1 {
			frameCount = int(packet[1] & 0x3f)
		}
	}
	return frameCount * opusFrameSamples(config)
}

func opusFrameSamples(config byte) int {
	switch {
	case config < 12:
		switch config % 4 {
		case 0:
			return 480
		case 1:
			return 960
		case 2:
			return 1920
		default:
			return 2880
		}
	case config < 16:
		if config%2 == 0 {
			return 480
		}
		return 960
	default:
		switch config % 4 {
		case 0:
			return 120
		case 1:
			return 240
		case 2:
			return 480
		default:
			return 960
		}
	}
}
