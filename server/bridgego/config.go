package bridgego

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	BridgeHost              string
	BridgePort              int
	STTURL                  string
	TTSURL                  string
	LLMURL                  string
	LLMModel                string
	LLMAPIKey               string
	IRAPIURL                string
	GeminiFallbackURL       string
	GeminiFallbackModel     string
	GeminiAPIKey            string
	TimezoneOffsetMinutes   int
	VoiceLockID             string
	TTSNumSteps             int
	AudioPacingSeconds      float64
	AudioPacingAheadPackets int
	MinTTSSeconds           float64
	MaxTTSSeconds           float64
	TTSSecondsPerChar       float64
	TTSRetryAttempts        int
	TTSRetryBackoffSeconds  float64
	LLMMaxTokens            int
	MaxConsecutiveNoInputs  int
	EnableLLMEndDetection   bool
	EventLogPath            string
	IRStatePath             string
}

func LoadConfig(envDirs ...string) Config {
	for _, dir := range envDirs {
		loadDotenv(dir)
	}
	return Config{
		BridgeHost:              strings.TrimSpace(os.Getenv("STACKCHAN_BRIDGE_HOST")),
		BridgePort:              envInt("STACKCHAN_BRIDGE_PORT", 8787),
		STTURL:                  envString("STACKCHAN_STT_URL", "http://127.0.0.1:8088/api/stt/v1/stt"),
		TTSURL:                  envString("STACKCHAN_TTS_URL", "http://127.0.0.1:8088/api/tts/v1/tts"),
		LLMURL:                  envString("STACKCHAN_LLM_URL", "http://127.0.0.1:8088/api/llm/v1/chat/completions"),
		LLMModel:                strings.TrimSpace(os.Getenv("STACKCHAN_LLM_MODEL")),
		LLMAPIKey:               strings.TrimSpace(os.Getenv("STACKCHAN_LLM_API_KEY")),
		IRAPIURL:                envString("STACKCHAN_IR_API_URL", "https://irremote-worker.kazu-san.workers.dev"),
		GeminiFallbackURL:       envString("STACKCHAN_GEMINI_FALLBACK_URL", "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"),
		GeminiFallbackModel:     envString("STACKCHAN_GEMINI_FALLBACK_MODEL", "gemini-2.5-flash-lite"),
		GeminiAPIKey:            firstEnv("STACKCHAN_GEMINI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY"),
		TimezoneOffsetMinutes:   envInt("STACKCHAN_TIMEZONE_OFFSET_MINUTES", 540),
		VoiceLockID:             strings.TrimSpace(os.Getenv("STACKCHAN_VOICE_LOCK_ID")),
		TTSNumSteps:             envInt("STACKCHAN_TTS_NUM_STEPS", 20),
		AudioPacingSeconds:      envFloat("STACKCHAN_AUDIO_PACING_SECONDS", float64(OutputFrameDurationMS)/1000.0),
		AudioPacingAheadPackets: envInt("STACKCHAN_AUDIO_PACING_AHEAD_PACKETS", 3),
		MinTTSSeconds:           envFloat("STACKCHAN_MIN_TTS_SECONDS", 5.5),
		MaxTTSSeconds:           envFloat("STACKCHAN_MAX_TTS_SECONDS", 18.0),
		TTSSecondsPerChar:       envFloat("STACKCHAN_TTS_SECONDS_PER_CHAR", 0.22),
		TTSRetryAttempts:        envInt("STACKCHAN_TTS_RETRY_ATTEMPTS", 3),
		TTSRetryBackoffSeconds:  envFloat("STACKCHAN_TTS_RETRY_BACKOFF_SECONDS", 0.75),
		LLMMaxTokens:            envInt("STACKCHAN_LLM_MAX_TOKENS", 2048),
		MaxConsecutiveNoInputs:  envInt("STACKCHAN_MAX_CONSECUTIVE_NO_INPUTS", 2),
		EnableLLMEndDetection:   envBool("STACKCHAN_ENABLE_LLM_END_DETECTION"),
		EventLogPath:            envString("STACKCHAN_BRIDGE_EVENT_LOG", filepath.Join(os.Getenv("HOME"), "stackchan_voice_bridge.events.log")),
		IRStatePath:             envString("STACKCHAN_IR_STATE_PATH", filepath.Join(os.Getenv("HOME"), ".config", "stackchan-swiftbar", "bridge_ir_state.json")),
	}
}

func loadDotenv(dir string) {
	if dir == "" {
		return
	}
	file, err := os.Open(filepath.Join(dir, ".env"))
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" || os.Getenv(key) != "" {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		_ = os.Setenv(key, value)
	}
}

func envString(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil {
		return fallback
	}
	return value
}

func envFloat(key string, fallback float64) float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv(key)), 64)
	if err != nil {
		return fallback
	}
	return value
}

func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}
