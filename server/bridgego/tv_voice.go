package bridgego

import (
	"context"
	"strings"
	"time"
)

func ParseLGTVVoiceCommand(text string) (string, bool) {
	normalized := normalizeVoicePhrase(text)
	commands := map[string]string{
		"テレビつけて": "power", "テレビをつけて": "power", "テレビ付けて": "power", "テレビを付けて": "power",
		"テレビ消して": "power", "テレビを消して": "power",
		"音量上げて": "volume_up", "音量を上げて": "volume_up",
		"音量下げて": "volume_down", "音量を下げて": "volume_down",
		"1チャンネル": "channel_1", "一チャンネル": "channel_1",
		"チャンネル1": "channel_1", "チャンネル一": "channel_1", "チャンネルを1にして": "channel_1",
		"2チャンネル": "channel_2", "二チャンネル": "channel_2",
		"チャンネル2": "channel_2", "チャンネル二": "channel_2", "チャンネルを2にして": "channel_2",
		"3チャンネル": "channel_3", "三チャンネル": "channel_3",
		"チャンネル3": "channel_3", "チャンネル三": "channel_3", "チャンネルを3にして": "channel_3",
	}
	action, ok := commands[normalized]
	return action, ok
}

func IsStackChanWakePhrase(text string) bool {
	switch normalizeVoicePhrase(text) {
	case "スタックちゃん", "すたっくちゃん":
		return true
	default:
		return false
	}
}

func normalizeVoicePhrase(text string) string {
	return strings.NewReplacer(" ", "", "　", "", "。", "", "、", "", "!", "", "！", "", "?", "", "？", "").Replace(strings.TrimSpace(text))
}

func (s *Session) HandleLGTVVoiceCommand(ctx context.Context, text string) (bool, error) {
	action, ok := ParseLGTVVoiceCommand(text)
	if !ok {
		return false, nil
	}
	_, err := s.CallMCP(map[string]any{
		"method": "tools/call",
		"params": map[string]any{
			"name":      "self.robot.send_lg_tv_command",
			"arguments": map[string]any{"action": action},
		},
	}, 5*time.Second)
	return true, err
}

func (s *Session) StartAmbientListening() error {
	return s.SendJSON(map[string]any{"type": "system", "command": "start_listening"})
}
