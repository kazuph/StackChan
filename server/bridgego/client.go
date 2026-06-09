package bridgego

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	cfg  Config
	http *http.Client
}

func NewClient(cfg Config) *Client {
	return &Client{
		cfg: cfg,
		http: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

func (c *Client) RunSTT(ctx context.Context, wavBytes []byte) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("language", "ja"); err != nil {
		return "", err
	}
	part, err := writer.CreateFormFile("file", "stackchan.wav")
	if err != nil {
		return "", err
	}
	if _, err := part.Write(wavBytes); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.STTURL, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", readHTTPError(resp)
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	return strings.TrimSpace(payload.Text), nil
}

func (c *Client) RunLLM(ctx context.Context, history []Message, userText, extraSystemPrompt string) (string, error) {
	systemPrompt := SystemPrompt
	if extraSystemPrompt != "" {
		systemPrompt += extraSystemPrompt
	}
	messages := make([]Message, 0, len(history)+2)
	messages = append(messages, Message{Role: "system", Content: systemPrompt})
	messages = append(messages, history...)
	messages = append(messages, Message{Role: "user", Content: userText})
	payload := chatRequest{
		Messages:    messages,
		Temperature: 0.6,
		MaxTokens:   c.cfg.LLMMaxTokens,
		Stop:        []string{"<think>", "</think>"},
	}
	response, err := c.requestLLMCompletion(ctx, payload)
	if err != nil {
		return "", err
	}
	return firstChoiceContent(response)
}

func (c *Client) RunIREventLLM(ctx context.Context, facts []string) (string, error) {
	payload := chatRequest{
		Messages: []Message{
			{Role: "system", Content: IREventPrompt},
			{Role: "user", Content: "解析できた操作: " + strings.Join(facts, "、")},
		},
		Temperature: 0.4,
		MaxTokens:   80,
	}
	response, err := c.requestLLMCompletion(ctx, payload)
	if err != nil {
		return "", err
	}
	content, err := firstChoiceContent(response)
	if err != nil {
		return "", err
	}
	return UsableIRLLMSpeech(content), nil
}

func (c *Client) GenerateAirconIR(ctx context.Context, cmd AirconCommand) ([]int, int, error) {
	payload := map[string]any{
		"protocol":     cmd.Protocol,
		"power":        cmd.Power,
		"mode":         cmd.Mode,
		"temperatureC": cmd.Temp,
		"fan":          cmd.Fan,
	}
	url := strings.TrimRight(c.cfg.IRAPIURL, "/") + "/api/generate"
	body, _, err := c.postJSONBytes(ctx, url, payload, "")
	if err != nil {
		return nil, 0, err
	}
	var response map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&response); err != nil {
		return nil, 0, err
	}
	rawValues, _ := response["raw"].([]any)
	raw := make([]int, 0, len(rawValues))
	for _, value := range rawValues {
		if timing := intFromAny(value); timing > 0 {
			raw = append(raw, timing)
		}
	}
	frequency := intFromAny(response["frequency"])
	return raw, frequency, nil
}

func (c *Client) RunStartupGreetingLLM(ctx context.Context) (string, error) {
	payload := chatRequest{
		Messages:    []Message{{Role: "system", Content: StartupGreetingPrompt}, {Role: "user", Content: "起床直後のひとことをお願いします。"}},
		Temperature: 0.9,
		MaxTokens:   64,
		Stop:        []string{"<think>", "</think>", "\n"},
	}
	response, err := c.requestLLMCompletion(ctx, payload)
	if err != nil {
		return "", err
	}
	return firstChoiceContent(response)
}

func (c *Client) ShouldEndConversation(ctx context.Context, history []Message, userText string) (bool, error) {
	if IsExitPhrase(userText) {
		return true, nil
	}
	if !c.cfg.EnableLLMEndDetection {
		return false, nil
	}
	messages := make([]Message, 0, len(history)+2)
	messages = append(messages, Message{Role: "system", Content: EndConversationPrompt})
	messages = append(messages, history...)
	messages = append(messages, Message{Role: "user", Content: userText})
	payload := chatRequest{
		Messages:    messages,
		Temperature: 0,
		MaxTokens:   8,
		Stop:        []string{"\n"},
	}
	response, err := c.requestLLMCompletion(ctx, payload)
	if err != nil {
		return false, err
	}
	content, err := firstChoiceContent(response)
	if err != nil {
		return false, err
	}
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(content)), "END"), nil
}

func (c *Client) RunTTS(ctx context.Context, text string) ([]byte, error) {
	requestedSeconds := EstimateTTSSeconds(c.cfg, text)
	payload := map[string]any{
		"text":      text,
		"seconds":   requestedSeconds,
		"num_steps": c.cfg.TTSNumSteps,
	}
	if c.cfg.VoiceLockID != "" {
		payload["voice_lock_id"] = c.cfg.VoiceLockID
	} else {
		payload["instruct"] = VoiceInstruct
	}

	var lastErr error
	for attempt := 1; attempt <= c.cfg.TTSRetryAttempts; attempt++ {
		wavBytes, statusCode, err := c.postJSONBytes(ctx, c.cfg.TTSURL, payload, "")
		if err == nil {
			return wavBytes, nil
		}
		lastErr = err
		if statusCode != http.StatusBadGateway && statusCode != http.StatusServiceUnavailable && statusCode != http.StatusGatewayTimeout {
			return nil, err
		}
		if attempt < c.cfg.TTSRetryAttempts {
			timer := time.NewTimer(time.Duration(c.cfg.TTSRetryBackoffSeconds*float64(attempt)*1000) * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return nil, lastErr
}

func (c *Client) requestLLMCompletion(ctx context.Context, payload chatRequest) (chatResponse, error) {
	primary := payload
	if c.cfg.LLMModel != "" {
		primary.Model = c.cfg.LLMModel
	}
	response, err := c.postJSONChat(ctx, c.cfg.LLMURL, primary, c.cfg.LLMAPIKey)
	if err == nil {
		return response, nil
	}
	primaryErr := err

	if c.cfg.GeminiAPIKey != "" && c.cfg.GeminiFallbackURL != "" && c.cfg.GeminiFallbackModel != "" {
		fallback := payload
		fallback.Model = c.cfg.GeminiFallbackModel
		fallback.ReasoningEffort = "minimal"
		return c.postJSONChat(ctx, c.cfg.GeminiFallbackURL, fallback, c.cfg.GeminiAPIKey)
	}
	return chatResponse{}, primaryErr
}

func (c *Client) postJSONChat(ctx context.Context, url string, payload chatRequest, apiKey string) (chatResponse, error) {
	body, statusCode, err := c.postJSONBytes(ctx, url, payload, apiKey)
	if err != nil {
		return chatResponse{}, err
	}
	if statusCode < 200 || statusCode >= 300 {
		return chatResponse{}, httpError{StatusCode: statusCode, URL: url, Body: string(body)}
	}
	var response chatResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return chatResponse{}, err
	}
	return response, nil
}

func (c *Client) postJSONBytes(ctx context.Context, url string, payload any, apiKey string) ([]byte, int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return responseBody, resp.StatusCode, httpError{StatusCode: resp.StatusCode, URL: url, Body: string(responseBody)}
	}
	return responseBody, resp.StatusCode, nil
}

func readHTTPError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	if len(body) > 240 {
		body = append(body[:240], []byte("...")...)
	}
	return httpError{StatusCode: resp.StatusCode, URL: resp.Request.URL.String(), Body: string(body)}
}

func firstChoiceContent(response chatResponse) (string, error) {
	if len(response.Choices) == 0 {
		return "", fmt.Errorf("llm response has no choices")
	}
	return strings.TrimSpace(response.Choices[0].Message.Content), nil
}
