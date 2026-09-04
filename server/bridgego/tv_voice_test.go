package bridgego

import "testing"

func TestParseLGTVVoiceCommand(t *testing.T) {
	tests := map[string]string{
		"テレビつけて": "power", "テレビを消して。": "power",
		"音量を上げて": "volume_up", "音量下げて！": "volume_down",
		"1 チャンネル": "channel_1", "二チャンネル": "channel_2", "3チャンネル。": "channel_3",
		"チャンネル1": "channel_1", "チャンネルを2にして": "channel_2", "チャンネル三": "channel_3",
		"4チャンネル": "channel_4", "五チャンネル": "channel_5", "チャンネル6": "channel_6",
		"チャンネル七": "channel_7", "チャンネルを8にして": "channel_8",
		"スタックちゃん、テレビをつけて": "power", "すたっくちゃん音量を下げて": "volume_down",
		"スタッフちゃん、テレビをつけて":    "power",
		"スタックちゃん、チャンネルを8にして": "channel_8",
		"テレビを消してテレビを消して":     "power",
		"音量下げて。何て言うの？":       "volume_down",
		"タクちゃん、テレビをつけて":      "power",
	}
	for input, want := range tests {
		got, ok := ParseLGTVVoiceCommand(input)
		if !ok || got != want {
			t.Fatalf("ParseLGTVVoiceCommand(%q)=(%q,%v), want (%q,true)", input, got, ok, want)
		}
	}
}

func TestParseLGTVVoiceCommandRejectsConversation(t *testing.T) {
	for _, input := range []string{"スタックちゃん", "テレビ見たい", "9チャンネル", "チャンネルを9にして", "テレビ消してから音量上げて", "スタックちゃんテレビ見たい"} {
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

func TestTVCommandAcknowledgementUsesDedicatedDisplayCommand(t *testing.T) {
	payload := TVCommandAcknowledgement("テレビ を つけ て")
	if payload["type"] != "system" || payload["command"] != "show_notification" || payload["text"] != "テレビをつけて" {
		t.Fatalf("TVCommandAcknowledgement()=%v", payload)
	}
}

func TestStackChanWakePhraseIsExplicit(t *testing.T) {
	for _, input := range []string{"スタックちゃん", "スタックちゃん？", "すたっくちゃん", "さっくちゃん", "タクちゃん", "スタッフちゃん", "ストックちゃん"} {
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

func TestStackChanWakePrefixStartsConversation(t *testing.T) {
	tests := map[string]string{
		"スタックちゃん、こんにちは": "こんにちは",
		"すたっくちゃんおはよう":   "おはよう",
		"さっくちゃんおはよう。":   "おはよう",
		"タクちゃん、こんにちは。":  "こんにちは",
		"スタッフちゃん、こんにちは": "こんにちは",
		"ストックちゃん、こんにちは": "こんにちは",
	}
	for input, want := range tests {
		got, ok := StripStackChanWakePrefix(input)
		if !ok || got != want {
			t.Fatalf("StripStackChanWakePrefix(%q)=(%q,%v), want (%q,true)", input, got, ok, want)
		}
	}
	for _, input := range []string{"テレビつけて", "ねえスタックちゃん", "洗濯ちゃんこんにちは"} {
		if remainder, ok := StripStackChanWakePrefix(input); ok {
			t.Fatalf("StripStackChanWakePrefix(%q) unexpectedly accepted %q", input, remainder)
		}
	}
}
