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
		"4チャンネル": "channel_4", "四チャンネル": "channel_4",
		"チャンネル4": "channel_4", "チャンネル四": "channel_4", "チャンネルを4にして": "channel_4",
		"5チャンネル": "channel_5", "五チャンネル": "channel_5",
		"チャンネル5": "channel_5", "チャンネル五": "channel_5", "チャンネルを5にして": "channel_5",
		"6チャンネル": "channel_6", "六チャンネル": "channel_6",
		"チャンネル6": "channel_6", "チャンネル六": "channel_6", "チャンネルを6にして": "channel_6",
		"7チャンネル": "channel_7", "七チャンネル": "channel_7",
		"チャンネル7": "channel_7", "チャンネル七": "channel_7", "チャンネルを7にして": "channel_7",
		"8チャンネル": "channel_8", "八チャンネル": "channel_8",
		"チャンネル8": "channel_8", "チャンネル八": "channel_8", "チャンネルを8にして": "channel_8",
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
