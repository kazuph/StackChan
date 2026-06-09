package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultEndpoint = "http://127.0.0.1:8787"
	defaultIRAPI    = "https://irremote-worker.kazu-san.workers.dev"
	carrierHz       = 38000
	minUsefulRawLen = 100
)

var acProtocolPrefixes = map[string]bool{
	"DAIKIN":     true,
	"FUJITSU":    true,
	"HITACHI":    true,
	"MIDEA":      true,
	"MITSUBISHI": true,
	"PANASONIC":  true,
	"SHARP":      true,
	"TOSHIBA":    true,
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("command is required")
	}
	switch args[0] {
	case "status":
		return printJSON(callTool("self.get_device_status", map[string]any{}, 5*time.Second))
	case "speak":
		fs := flag.NewFlagSet("speak", flag.ContinueOnError)
		text := fs.String("text", "", "")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return printJSON(bridgePost("/speak", map[string]any{"text": *text}, 5*time.Second))
	case "announce-ir":
		fs := flag.NewFlagSet("announce-ir", flag.ContinueOnError)
		payloadText := fs.String("payload", "", "")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(*payloadText), &payload); err != nil {
			return err
		}
		return printJSON(bridgePost("/ir/decode-speech", payload, 5*time.Second))
	case "reset-receiver":
		if _, err := callTool("self.robot.reset_ir_receiver", map[string]any{}, 5*time.Second); err != nil {
			return err
		}
		return printJSON(map[string]any{"ok": true, "reset": "ir_receiver"}, nil)
	case "decode-mcp-latest":
		fs := flag.NewFlagSet("decode-mcp-latest", flag.ContinueOnError)
		after := fs.Int("after-frame-count", 0, "")
		_ = fs.Float64("max-age", 30, "")
		_ = fs.Int("min-durations", 100, "")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return decodeMCPLatest(*after)
	case "watch-mcp-ir":
		fs := flag.NewFlagSet("watch-mcp-ir", flag.ContinueOnError)
		interval := fs.Float64("interval", 0.4, "")
		_ = fs.Float64("max-age", 180, "")
		_ = fs.Int("min-durations", 100, "")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return watchMCPIR(time.Duration(*interval * float64(time.Second)))
	case "send":
		fs := flag.NewFlagSet("send", flag.ContinueOnError)
		protocol := fs.String("protocol", "", "")
		power := fs.String("power", "on", "")
		mode := fs.String("mode", "cool", "")
		temp := fs.Int("temp", 26, "")
		fan := fs.String("fan", "auto", "")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return send(*protocol, *power == "on", *mode, *temp, *fan)
	default:
		return fmt.Errorf("unknown command: %s", args[0])
	}
}

func endpoint() string {
	if value := strings.TrimSpace(os.Getenv("STACKCHAN_MCP_ENDPOINT")); value != "" {
		return strings.TrimRight(value, "/")
	}
	path := filepath.Join(os.Getenv("HOME"), ".config", "stackchan-swiftbar", "endpoint.txt")
	if data, err := os.ReadFile(path); err == nil {
		if value := strings.TrimSpace(string(data)); value != "" {
			return strings.TrimRight(value, "/")
		}
	}
	return defaultEndpoint
}

func irAPIEndpoint() string {
	if value := strings.TrimSpace(os.Getenv("IRREMOTE_API_ENDPOINT")); value != "" {
		return strings.TrimRight(value, "/")
	}
	return defaultIRAPI
}

func bridgePost(path string, payload any, timeout time.Duration) (map[string]any, error) {
	return postJSON(endpoint()+path, payload, timeout)
}

func apiPost(path string, payload any, timeout time.Duration) (map[string]any, error) {
	return postJSON(irAPIEndpoint()+path, payload, timeout)
}

func postJSON(url string, payload any, timeout time.Duration) (map[string]any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "StackChanIRRemote/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	var out map[string]any
	dec := json.NewDecoder(bytes.NewReader(respBody))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func callTool(name string, arguments map[string]any, timeout time.Duration) (map[string]any, error) {
	return bridgePost("/mcp/call", map[string]any{"name": name, "arguments": arguments, "timeout": timeout.Seconds()}, timeout+3*time.Second)
}

func mcpText(result map[string]any) (string, error) {
	resultObj, _ := result["result"].(map[string]any)
	content, _ := resultObj["content"].([]any)
	if len(content) == 0 {
		return "", fmt.Errorf("missing MCP text content: %v", result)
	}
	first, _ := content[0].(map[string]any)
	return toString(first["text"]), nil
}

func decodeMCPLatest(afterFrame int) error {
	result, err := callTool("self.robot.get_ir_rx_latest", map[string]any{}, 5*time.Second)
	if err != nil {
		return err
	}
	text, err := mcpText(result)
	if err != nil {
		return err
	}
	var latest map[string]any
	if err := json.Unmarshal([]byte(text), &latest); err != nil {
		return err
	}
	payload, err := decodedFromMCP(latest, afterFrame)
	if err != nil {
		return err
	}
	return printJSON(payload, nil)
}

func watchMCPIR(interval time.Duration) error {
	lastFrame := 0
	if status, err := getIRStatus(); err == nil {
		lastFrame = intValue(status["frame_count"])
	}
	lastHeartbeat := time.Time{}
	for {
		status, err := getIRStatus()
		if err != nil {
			_ = printJSON(map[string]any{"event": "error", "error": err.Error()}, nil)
			time.Sleep(maxDuration(time.Second, interval))
			continue
		}
		frame := intValue(status["frame_count"])
		if time.Since(lastHeartbeat) >= 10*time.Second {
			_ = printJSON(map[string]any{
				"event":                "heartbeat",
				"frame_count":          frame,
				"receiver_configured":  status["receiver_configured"],
				"queue_drop_count":     status["queue_drop_count"],
				"overflow_frame_count": status["overflow_frame_count"],
			}, nil)
			lastHeartbeat = time.Now()
		}
		if frame > lastFrame {
			result, err := callTool("self.robot.get_ir_rx_latest", map[string]any{}, 5*time.Second)
			if err != nil {
				_ = printJSON(map[string]any{"event": "error", "error": err.Error()}, nil)
			} else if text, err := mcpText(result); err == nil {
				var latest map[string]any
				if err := json.Unmarshal([]byte(text), &latest); err == nil {
					if payload, err := decodedFromMCP(latest, lastFrame); err == nil {
						_ = printJSON(payload, nil)
					} else {
						_ = printJSON(map[string]any{"event": "error", "error": err.Error()}, nil)
					}
				}
			}
			lastFrame = frame
		}
		time.Sleep(interval)
	}
}

func getIRStatus() (map[string]any, error) {
	result, err := callTool("self.robot.get_ir_rx_status", map[string]any{}, 4*time.Second)
	if err != nil {
		return nil, err
	}
	text, err := mcpText(result)
	if err != nil {
		return nil, err
	}
	var status map[string]any
	if err := json.Unmarshal([]byte(text), &status); err != nil {
		return nil, err
	}
	return status, nil
}

func decodedFromMCP(latest map[string]any, afterFrame int) (map[string]any, error) {
	frame := intValue(firstValue(latest, "frame_count", "decode_count"))
	if frame <= 0 {
		return nil, errors.New("no IR captures yet")
	}
	if afterFrame > 0 && frame <= afterFrame {
		return nil, errors.New("no newer IR frame yet")
	}
	raw := strings.TrimSpace(toString(latest["raw_usec"]))
	durations := intValue(firstValue(latest, "durations", "rawlen"))
	age := float64(intValue(latest["age_ms"])) / 1000.0
	if durations > 0 && durations < minUsefulRawLen {
		return map[string]any{"ok": false, "ignored": true, "short_frame": true, "protocol": "UNKNOWN", "manufacturer": "Unknown", "frame_count": frame, "age_sec": age, "captured_durations": durations, "raw_usec": raw, "source": "mcp-irremoteesp8266", "error": fmt.Sprintf("short non-AC frame ignored: durations=%d", durations)}, nil
	}
	if raw == "" {
		return nil, errors.New("latest IR frame has no raw_usec")
	}
	decoded, err := inferRaw(raw)
	if err != nil {
		return nil, err
	}
	decoded["frame_count"] = frame
	decoded["age_sec"] = age
	decoded["captured_durations"] = durations
	decoded["raw_usec"] = raw
	if ok, _ := decoded["ok"].(bool); !ok {
		decoded["ignored"] = true
		decoded["error"] = "IRremote Web API did not match this frame"
	}
	return decoded, nil
}

func inferRaw(raw string) (map[string]any, error) {
	values, err := rawStringToList(raw)
	if err != nil {
		return nil, err
	}
	resp, err := apiPost("/api/infer", map[string]any{"raw": values, "frequency": carrierHz}, 12*time.Second)
	if err != nil {
		return nil, err
	}
	decoded, _ := resp["decoded"].(map[string]any)
	if decoded == nil {
		decoded = map[string]any{}
	}
	state, _ := resp["state"].([]any)
	matched, _ := resp["matched"].(bool)
	acMatched := matched && isSupportedACResult(resp, decoded)
	return map[string]any{
		"ok":             acMatched,
		"protocol":       stringOr(resp["protocol"], "UNKNOWN"),
		"manufacturer":   stringOr(resp["manufacturer"], "Unknown"),
		"model":          resp["model"],
		"models":         resp["models"],
		"bits":           len(state) * 8,
		"state":          state,
		"description":    decodedDescription(decoded),
		"decoded":        decoded,
		"confidence":     resp["confidence"],
		"candidates":     resp["candidates"],
		"supported_send": acMatched,
		"source":         "irremote-worker",
	}, nil
}

func isSupportedACResult(resp map[string]any, decoded map[string]any) bool {
	manufacturer := strings.ToUpper(strings.TrimSpace(toString(resp["manufacturer"])))
	if acProtocolPrefixes[manufacturer] {
		return true
	}
	protocol := strings.ToUpper(strings.TrimSpace(toString(resp["protocol"])))
	prefix, _, _ := strings.Cut(protocol, "_")
	if acProtocolPrefixes[prefix] {
		return true
	}
	return len(decoded) > 0 && protocol != "" && protocol != "UNKNOWN" && protocol != "MULTIBRACKETS"
}

func send(protocol string, power bool, mode string, temp int, fan string) error {
	if strings.TrimSpace(protocol) == "" {
		return errors.New("protocol is required for IR generation")
	}
	payload := map[string]any{"protocol": protocol, "power": power, "mode": mode, "temperatureC": temp, "fan": apiFanValue(fan)}
	routeLog := []string{
		fmt.Sprintf("UI/helper: send protocol=%s power=%t mode=%s temp=%d fan=%s", protocol, power, mode, temp, fan),
		fmt.Sprintf("IR API generate: %s/api/generate", irAPIEndpoint()),
	}
	generated, err := apiPost("/api/generate", payload, 12*time.Second)
	if err != nil {
		return err
	}
	rawList, _ := generated["raw"].([]any)
	if len(rawList) == 0 {
		return fmt.Errorf("IRremote API returned no raw timings: %v", generated)
	}
	raw := make([]string, 0, len(rawList))
	totalUsec := 0
	for _, value := range rawList {
		if timing := intValue(value); timing > 0 {
			raw = append(raw, strconv.Itoa(timing))
			totalUsec += timing
		}
	}
	frequency := intValue(generated["frequency"])
	if frequency == 0 {
		frequency = carrierHz
	}
	routeLog = append(routeLog, fmt.Sprintf("IR API generated raw: durations=%d carrier=%dHz", len(rawList), frequency))
	routeLog = append(routeLog, fmt.Sprintf("Raw send timing: durations=%d total=%.1fms", len(raw), float64(totalUsec)/1000.0))
	routeLog = append(routeLog, "MCP send: self.robot.send_ir_raw")
	result, err := callTool("self.robot.send_ir_raw", map[string]any{"timings_usec": strings.Join(raw, ","), "carrier_hz": frequency}, 20*time.Second)
	if err != nil {
		return err
	}
	if err := ensureMCPResultOK(result); err != nil {
		return err
	}
	sendText, _ := mcpText(result)
	var firmwareProof map[string]any
	if err := json.Unmarshal([]byte(sendText), &firmwareProof); err == nil {
		routeLog = append(routeLog, fmt.Sprintf("StackChan firmware: %s %s gpio=%v timings=%v carrier=%vHz",
			toString(firmwareProof["driver"]),
			toString(firmwareProof["function"]),
			firmwareProof["gpio"],
			firmwareProof["timing_count"],
			firmwareProof["carrier_hz"],
		))
	} else {
		routeLog = append(routeLog, "StackChan firmware send result: "+sendText)
	}
	time.Sleep(1500 * time.Millisecond)
	clearResult, clearErr := callTool("self.robot.reset_ir_receiver", map[string]any{}, 5*time.Second)
	if clearErr != nil {
		routeLog = append(routeLog, "RX clear after send: error "+clearErr.Error())
	} else {
		clearText, _ := mcpText(clearResult)
		routeLog = append(routeLog, "RX clear after send: "+clearText)
	}
	return printJSON(map[string]any{"ok": true, "protocol": stringOr(generated["protocol"], protocol), "manufacturer": generated["manufacturer"], "model": generated["model"], "mode": "generated_by_irremote_web_api", "frequency": frequency, "durations": len(raw), "settings": firstValue(generated, "settings"), "route_log": routeLog, "firmware_proof": firmwareProof}, nil)
}

func ensureMCPResultOK(result map[string]any) error {
	if errObj, ok := result["error"]; ok && errObj != nil {
		return fmt.Errorf("MCP error: %v", errObj)
	}
	resultObj, _ := result["result"].(map[string]any)
	if boolValue(resultObj["isError"]) {
		text, _ := mcpText(result)
		if text == "" {
			text = fmt.Sprint(resultObj)
		}
		return fmt.Errorf("MCP tool error: %s", text)
	}
	return nil
}

func decodedDescription(decoded map[string]any) string {
	var parts []string
	if _, ok := decoded["power"]; ok {
		if boolValue(decoded["power"]) {
			parts = append(parts, "Power: On")
		} else {
			parts = append(parts, "Power: Off")
		}
	}
	if mode := toString(decoded["mode"]); mode != "" {
		parts = append(parts, "Mode: "+mode)
	}
	if temp := toString(decoded["temperatureC"]); temp != "" {
		parts = append(parts, "Temp: "+temp+"C")
	}
	if fan := toString(decoded["fan"]); fan != "" {
		parts = append(parts, "Fan: "+fan)
	}
	if _, ok := decoded["checksumOk"]; ok {
		if boolValue(decoded["checksumOk"]) {
			parts = append(parts, "Checksum: OK")
		} else {
			parts = append(parts, "Checksum: NG")
		}
	}
	return strings.Join(parts, ", ")
}

func rawStringToList(raw string) ([]int, error) {
	var out []int
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		value, err := strconv.Atoi(part)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, nil
}

func printJSON(value map[string]any, err error) error {
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}

func apiFanValue(fan string) string {
	if fan == "silent" {
		return "low"
	}
	return fan
}

func firstValue(m map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := m[key]; ok {
			return value
		}
	}
	return nil
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
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprint(v)
	}
}

func intValue(value any) int {
	switch v := value.(type) {
	case json.Number:
		i, _ := strconv.Atoi(v.String())
		return i
	case float64:
		return int(v)
	case int:
		return v
	case string:
		i, _ := strconv.Atoi(v)
		return i
	default:
		return 0
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
	default:
		return false
	}
}

func stringOr(value any, fallback string) string {
	if text := toString(value); text != "" {
		return text
	}
	return fallback
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
