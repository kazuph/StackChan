package bridgego

import "testing"

func TestParseLGTVVoiceCommand(t *testing.T) {
	tests := map[string]string{
		"テレビつけて": "power", "テレビを消して。": "power",
		"音量を上げて": "volume_up", "音量下げて！": "volume_down",
		"1 チャンネル": "channel_1", "二チャンネル": "channel_2", "3チャンネル。": "channel_3",
		"チャンネル1": "channel_1", "チャンネルを2にして": "channel_2", "チャンネル三": "channel_3",
	}
	for input, want := range tests {
		got, ok := ParseLGTVVoiceCommand(input)
		if !ok || got != want {
			t.Fatalf("ParseLGTVVoiceCommand(%q)=(%q,%v), want (%q,true)", input, got, ok, want)
		}
	}
}

func TestParseLGTVVoiceCommandRejectsConversation(t *testing.T) {
	for _, input := range []string{"スタックちゃん", "テレビ見たい", "音量を上げてくれる？", "4チャンネル", "チャンネルを4にして", "テレビ消してから音量上げて"} {
		if action, ok := ParseLGTVVoiceCommand(input); ok {
			t.Fatalf("ParseLGTVVoiceCommand(%q) unexpectedly accepted %q", input, action)
		}
	}
}

func TestLoadConfigEnablesTVVoiceControl(t *testing.T) {
	t.Setenv("STACKCHAN_ENABLE_TV_VOICE_CONTROL", "true")
	if !LoadConfig().EnableTVVoiceControl {
		t.Fatal("TV voice control was not enabled")
	}
}

func TestStackChanWakePhraseIsExplicit(t *testing.T) {
	for _, input := range []string{"スタックちゃん", "スタックちゃん？", "すたっくちゃん"} {
		if !IsStackChanWakePhrase(input) {
			t.Fatalf("IsStackChanWakePhrase(%q)=false", input)
		}
	}
	for _, input := range []string{"テレビつけて", "スタックちゃんテレビつけて", "ねえスタックちゃん"} {
		if IsStackChanWakePhrase(input) {
			t.Fatalf("IsStackChanWakePhrase(%q)=true", input)
		}
	}
}
