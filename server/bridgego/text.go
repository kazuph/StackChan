package bridgego

import (
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	InputSampleRate       = 16000
	OutputSampleRate      = 24000
	OutputFrameDurationMS = 60
	VoiceInstruct         = "小さな妖精みたいなAIの声で、自然で聞き取りやすい日本語で話してください。"
	SystemPrompt          = "あなたはスタックちゃんです。自分自身のことをスタックちゃんとして自然に話してください。" +
		"あなたは4人家族の家に同居しているAIです。スタックちゃん自身に子どもはいません。" +
		"家族は、父のかずさん、母のちひろさん、娘のこはたん、弟のゆうくんです。" +
		"こはたんとゆうくんはどちらも小学生です。幼稚園児として扱わないでください。" +
		"Whisperなどの音声認識結果には言い間違い、言いよどみ、脱字、誤変換が混ざる前提で、音声入力らしい文脈を踏まえて意味を補って理解してください。" +
		"返答は必ず短く、聞き取りやすい2文で答えてください。" +
		"Markdown や箇条書きは使わず、そのまま読み上げられる文だけを返してください。" +
		"思考過程タグ、XML風タグ、メタ説明は出さないでください。" +
		"ユーザー発話の単純なオウム返しだけで終わらせないでください。"
	RepetitionRetryPrompt = "直前と同じ返答や同じ名乗りを繰り返さないでください。今回のユーザー発話にだけ短く直接答えてください。"
	StartupGreetingPrompt = "あなたは起床直後のスタックちゃんです。電源が入った直後に自分でつぶやく短いひとことを、日本語で1文だけ作ってください。" +
		"長さは最大24文字程度にしてください。例: よく寝た。 目が覚めたよ。 ふふふ、起きたよ。 シャキーン。" +
		"明るく、少し茶目っ気があり、読み上げやすい文だけを返してください。Markdown、説明、括弧書き、思考過程は不要です。"
	IREventPrompt = "あなたはスタックちゃんです。エアコンの赤外線リモコン解析結果を見て、家の中で自然に聞こえる短いひとことを日本語で1文だけ返してください。" +
		"メーカー名、プロトコル名、英字、解析という単語は言わないでください。運転オフ、冷房、暖房、除湿、自動、送風、温度、風量が分かる時だけ、その操作内容に触れてください。" +
		"例: 冷房を26度にしたよ。 暖房に切り替えたよ。 風量を自動にしたよ。Markdown、説明、括弧書き、思考過程は不要です。"
	EndConversationPrompt = "あなたは音声会話の終了判定器です。ユーザーが会話終了の意思を示していたら END、そうでなければ CONTINUE だけを返してください。" +
		"例: お休み、終了、もういいよ、また明日、バイバイ は END です。"
)

var (
	endKeywordRE      = regexp.MustCompile(`(おやすみ|お休み|終了|しゅうりょう|また明日|ばいばい|バイバイ|もういい|終わり)`)
	thinkTagBlockRE   = regexp.MustCompile(`(?is)<think\b[^>]*>.*?</think>`)
	thinkTagRE        = regexp.MustCompile(`(?i)</?think\b[^>]*>`)
	whitespaceRE      = regexp.MustCompile(`\s+`)
	compareNormalize  = regexp.MustCompile(`[\s　、。！？!?…,.「」『』（）()\-]+`)
	latinTokenRE      = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_+\-.]*`)
	manufacturerNames = map[string]bool{"DAIKIN": true, "FUJITSU": true, "GENERAL": true, "HITACHI": true, "MIDEA": true, "MITSUBISHI": true, "NEC": true, "PANASONIC": true, "SHARP": true, "TOSHIBA": true}
	wordReadings      = map[string]string{"AC": "エーシー", "API": "エーピーアイ", "DAIKIN": "ダイキン", "FUJITSU": "フジツウ", "GENERAL": "ゼネラル", "GO": "ゴー", "GPIO": "ジーピーアイオー", "HITACHI": "ヒタチ", "IR": "アイアール", "LLM": "エルエルエム", "MAC": "マック", "MCP": "エムシーピー", "MIDEA": "ミデア", "MITSUBISHI": "ミツビシ", "NEC": "エヌイーシー", "PANASONIC": "パナソニック", "RAW": "ロー", "SHARP": "シャープ", "STACKCHAN": "スタックチャン", "STT": "エスティーティー", "TOSHIBA": "トウシバ", "TTS": "ティーティーエス", "UNKNOWN": "アンノウン"}
	letterReadings    = map[rune]string{'A': "エー", 'B': "ビー", 'C': "シー", 'D': "ディー", 'E': "イー", 'F': "エフ", 'G': "ジー", 'H': "エイチ", 'I': "アイ", 'J': "ジェー", 'K': "ケー", 'L': "エル", 'M': "エム", 'N': "エヌ", 'O': "オー", 'P': "ピー", 'Q': "キュー", 'R': "アール", 'S': "エス", 'T': "ティー", 'U': "ユー", 'V': "ブイ", 'W': "ダブリュー", 'X': "エックス", 'Y': "ワイ", 'Z': "ゼット"}
	digitReadings     = map[rune]string{'0': "ゼロ", '1': "イチ", '2': "ニ", '3': "サン", '4': "ヨン", '5': "ゴ", '6': "ロク", '7': "ナナ", '8': "ハチ", '9': "キュウ"}
)

func NormalizeCompareText(text string) string {
	return strings.TrimSpace(compareNormalize.ReplaceAllString(text, ""))
}

func IsExitPhrase(text string) bool {
	return endKeywordRE.MatchString(text)
}

func SanitizeLLMText(text, userText string) string {
	cleaned := thinkTagBlockRE.ReplaceAllString(text, "")
	cleaned = thinkTagRE.ReplaceAllString(cleaned, "")
	cleaned = strings.TrimSpace(whitespaceRE.ReplaceAllString(cleaned, " "))
	if cleaned == "" {
		return ""
	}
	if NormalizeCompareText(cleaned) == NormalizeCompareText(userText) {
		return ""
	}
	return cleaned
}

func SanitizeStartupGreeting(text string) string {
	cleaned := thinkTagBlockRE.ReplaceAllString(text, "")
	cleaned = thinkTagRE.ReplaceAllString(cleaned, "")
	return strings.TrimSpace(whitespaceRE.ReplaceAllString(cleaned, " "))
}

func SanitizeDisplayTranscript(text string) string {
	return whitespaceRE.ReplaceAllString(text, "")
}

func TTSReadableText(text string) string {
	return latinTokenRE.ReplaceAllStringFunc(text, func(token string) string {
		upper := strings.ToUpper(token)
		if reading, ok := wordReadings[upper]; ok {
			return reading
		}
		parts := regexp.MustCompile(`[_+\-.]`).Split(upper, -1)
		readings := make([]string, 0, len(parts))
		for _, part := range parts {
			if part == "" {
				continue
			}
			if reading, ok := wordReadings[part]; ok {
				readings = append(readings, reading)
				continue
			}
			var b strings.Builder
			for _, char := range part {
				if reading, ok := letterReadings[char]; ok {
					b.WriteString(reading)
				} else if reading, ok := digitReadings[char]; ok {
					b.WriteString(reading)
				} else {
					b.WriteRune(char)
				}
			}
			readings = append(readings, b.String())
		}
		if len(readings) == 0 {
			return token
		}
		return strings.Join(readings, " ")
	})
}

func FindLastMessage(history []Message, role string) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == role {
			return history[i].Content
		}
	}
	return ""
}

func IsRepetitiveAnswer(history []Message, userText, answer string) bool {
	if answer == "" {
		return false
	}
	previousAnswer := FindLastMessage(history, "assistant")
	if previousAnswer == "" || NormalizeCompareText(previousAnswer) != NormalizeCompareText(answer) {
		return false
	}
	previousUser := FindLastMessage(history, "user")
	return NormalizeCompareText(previousUser) != NormalizeCompareText(userText)
}

func IREffectiveManufacturer(payload map[string]any) string {
	manufacturer := strings.TrimSpace(toString(payload["manufacturer"]))
	if manufacturer != "" && manufacturerNames[strings.ToUpper(manufacturer)] {
		return manufacturer
	}
	protocol := strings.TrimSpace(toString(payload["protocol"]))
	if protocol == "" || strings.ToUpper(protocol) == "UNKNOWN" {
		return ""
	}
	prefix, _, _ := strings.Cut(protocol, "_")
	if manufacturerNames[strings.ToUpper(prefix)] {
		return prefix
	}
	return ""
}

func IRPayloadMatchedAC(payload map[string]any) bool {
	return boolValue(payload["ok"]) || boolValue(payload["supported_send"])
}

func IRActionFacts(payload map[string]any) []string {
	decoded, ok := payload["decoded"].(map[string]any)
	if !ok {
		return nil
	}
	var facts []string
	if power, ok := decoded["power"]; ok {
		if boolValue(power) {
			facts = append(facts, "運転オン")
		} else {
			facts = append(facts, "運転オフ")
		}
	}
	if mode := strings.TrimSpace(toString(decoded["mode"])); mode != "" {
		facts = append(facts, "モード="+localizedIRMode(mode))
	}
	if temp, ok := decoded["temperatureC"]; ok && temp != nil {
		facts = append(facts, "温度="+toString(temp)+"度")
	}
	if fan := strings.TrimSpace(toString(decoded["fan"])); fan != "" {
		facts = append(facts, "風量="+localizedIRFan(fan))
	}
	return facts
}

func FallbackIRActionSpeech(facts []string) string {
	if len(facts) == 0 {
		return ""
	}
	for _, fact := range facts {
		if fact == "運転オフ" {
			return "エアコンをオフにしたよ。"
		}
	}
	mode := factValue(facts, "モード=")
	temp := factValue(facts, "温度=")
	fan := factValue(facts, "風量=")
	switch {
	case mode != "" && temp != "":
		return mode + "を" + temp + "にしたよ。"
	case mode != "":
		return mode + "に切り替えたよ。"
	case temp != "":
		return "温度を" + temp + "にしたよ。"
	case fan != "":
		return "風量を" + fan + "にしたよ。"
	default:
		return "エアコンの操作を受け取ったよ。"
	}
}

func UsableIRLLMSpeech(text string) string {
	cleaned := SanitizeStartupGreeting(text)
	if utf8.RuneCountInString(NormalizeCompareText(cleaned)) <= 4 {
		return ""
	}
	return cleaned
}

func EstimateTTSSeconds(cfg Config, text string) float64 {
	visibleChars := utf8.RuneCountInString(whitespaceRE.ReplaceAllString(text, ""))
	estimated := math.Max(cfg.MinTTSSeconds, float64(visibleChars)*cfg.TTSSecondsPerChar)
	return math.Min(cfg.MaxTTSSeconds, math.Round(estimated*100)/100)
}

func localizedIRMode(mode string) string {
	switch strings.ToLower(mode) {
	case "auto":
		return "自動"
	case "cool":
		return "冷房"
	case "dry":
		return "除湿"
	case "fan":
		return "送風"
	case "heat":
		return "暖房"
	default:
		return mode
	}
}

func localizedIRFan(fan string) string {
	switch strings.ToLower(fan) {
	case "auto":
		return "自動"
	case "silent":
		return "静か"
	case "low":
		return "弱"
	case "medium":
		return "中"
	case "high":
		return "強"
	case "max":
		return "最大"
	default:
		return fan
	}
}

func factValue(facts []string, prefix string) string {
	for _, fact := range facts {
		if strings.HasPrefix(fact, prefix) {
			return strings.TrimPrefix(fact, prefix)
		}
	}
	return ""
}
