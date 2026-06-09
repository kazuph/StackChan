package bridgego

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Messages        []Message `json:"messages"`
	Temperature     float64   `json:"temperature"`
	MaxTokens       int       `json:"max_tokens"`
	Stop            []string  `json:"stop,omitempty"`
	Model           string    `json:"model,omitempty"`
	ReasoningEffort string    `json:"reasoning_effort,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
}

type httpError struct {
	StatusCode int
	URL        string
	Body       string
}

func (e httpError) Error() string {
	return fmt.Sprintf("http status %d from %s: %s", e.StatusCode, e.URL, e.Body)
}

func UserSafeAlertMessage(err error) string {
	var httpErr httpError
	if errors.As(err, &httpErr) {
		switch {
		case strings.Contains(httpErr.URL, "/api/tts/"):
			return "音声合成でエラーが出たよ。もう一度ためしてね。"
		case strings.Contains(httpErr.URL, "/api/stt/"):
			return "聞き取りでエラーが出たよ。もう一度話してね。"
		case strings.Contains(httpErr.URL, "/api/llm/") || strings.Contains(httpErr.URL, "/chat/completions"):
			return "考えるところでエラーが出たよ。もう一度ためしてね。"
		}
	}
	return "橋渡しでエラーが出たよ。もう一度ためしてね。"
}

func toString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprint(v)
	}
}

func boolValue(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		parsed, _ := strconv.ParseBool(v)
		return parsed
	case float64:
		return v != 0
	case int:
		return v != 0
	default:
		return false
	}
}

func intFromAny(value any) int {
	switch v := value.(type) {
	case json.Number:
		i, _ := strconv.Atoi(v.String())
		return i
	case float64:
		return int(v)
	case float32:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case string:
		i, _ := strconv.Atoi(strings.TrimSpace(v))
		return i
	default:
		return 0
	}
}
