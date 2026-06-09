package bridgego

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const DefaultIRCarrierHz = 38000

var (
	japaneseTempRE = regexp.MustCompile(`([12][0-9]|3[01])\s*度`)
)

type AirconCommand struct {
	Protocol string
	Power    bool
	Mode     string
	Temp     int
	Fan      string
	Reason   string
}

func DefaultAirconCommand(protocol string) AirconCommand {
	return AirconCommand{Protocol: protocol, Power: true, Mode: "cool", Temp: 26, Fan: "auto"}
}

func AirconCommandFromText(text string, current AirconCommand) (AirconCommand, bool) {
	normalized := strings.TrimSpace(text)
	if normalized == "" {
		return AirconCommand{}, false
	}
	cmd := current
	if cmd.Temp == 0 {
		cmd.Temp = 26
	}
	if cmd.Mode == "" {
		cmd.Mode = "cool"
	}
	if cmd.Fan == "" {
		cmd.Fan = "auto"
	}

	lower := strings.ToLower(normalized)
	mentionedAircon := strings.Contains(normalized, "エアコン") || strings.Contains(normalized, "冷房") ||
		strings.Contains(normalized, "暖房") || strings.Contains(normalized, "除湿") ||
		strings.Contains(normalized, "送風") || strings.Contains(normalized, "風量") ||
		strings.Contains(normalized, "温度") || strings.Contains(normalized, "暑") ||
		strings.Contains(normalized, "寒") || strings.Contains(lower, "aircon") ||
		strings.Contains(lower, "ac")
	if !mentionedAircon {
		return AirconCommand{}, false
	}

	switch {
	case strings.Contains(normalized, "消して") || strings.Contains(normalized, "切って") ||
		strings.Contains(normalized, "止めて") || strings.Contains(normalized, "オフ") ||
		strings.Contains(lower, "off"):
		cmd.Power = false
		cmd.Reason = "power_off"
		return cmd, true
	default:
		cmd.Power = true
	}

	switch {
	case strings.Contains(normalized, "冷房") || strings.Contains(normalized, "暑"):
		cmd.Mode = "cool"
		cmd.Reason = "cool"
	case strings.Contains(normalized, "暖房") || strings.Contains(normalized, "寒"):
		cmd.Mode = "heat"
		cmd.Reason = "heat"
	case strings.Contains(normalized, "除湿") || strings.Contains(normalized, "ドライ"):
		cmd.Mode = "dry"
		cmd.Reason = "dry"
	case strings.Contains(normalized, "送風"):
		cmd.Mode = "fan"
		cmd.Reason = "fan"
	case strings.Contains(normalized, "自動"):
		cmd.Mode = "auto"
		cmd.Reason = "auto"
	}

	if match := japaneseTempRE.FindStringSubmatch(normalized); len(match) == 2 {
		if temp, err := strconv.Atoi(match[1]); err == nil {
			cmd.Temp = clampTemp(temp)
			cmd.Reason = "temperature"
		}
	}
	if strings.Contains(normalized, "温度上げ") || strings.Contains(normalized, "温度を上げ") || strings.Contains(normalized, "上げて") {
		cmd.Temp = clampTemp(cmd.Temp + 1)
		cmd.Reason = "temperature_up"
	}
	if strings.Contains(normalized, "温度下げ") || strings.Contains(normalized, "温度を下げ") || strings.Contains(normalized, "下げて") {
		cmd.Temp = clampTemp(cmd.Temp - 1)
		cmd.Reason = "temperature_down"
	}

	switch {
	case strings.Contains(normalized, "風量自動"):
		cmd.Fan = "auto"
		cmd.Reason = "fan_auto"
	case strings.Contains(normalized, "風量弱") || strings.Contains(normalized, "弱風"):
		cmd.Fan = "low"
		cmd.Reason = "fan_low"
	case strings.Contains(normalized, "風量中"):
		cmd.Fan = "medium"
		cmd.Reason = "fan_medium"
	case strings.Contains(normalized, "風量強") || strings.Contains(normalized, "強風"):
		cmd.Fan = "high"
		cmd.Reason = "fan_high"
	}

	if cmd.Reason == "" {
		cmd.Reason = "aircon"
	}
	return cmd, true
}

func (s *Session) HandleAirconCommand(ctx context.Context, userText string) (bool, string) {
	s.stateMu.Lock()
	protocol := s.lastIRProtocol
	if protocol == "" && s.lastIRManufacturer != "" {
		protocol = s.lastIRManufacturer
	}
	current := DefaultAirconCommand(protocol)
	if s.lastAircon.Protocol != "" {
		current = s.lastAircon
	}
	if current.Protocol == "" {
		current.Protocol = protocol
	}
	s.stateMu.Unlock()

	cmd, ok := AirconCommandFromText(userText, current)
	if !ok {
		return false, ""
	}
	if strings.TrimSpace(cmd.Protocol) == "" {
		return true, "先にエアコンのリモコン信号を受信させてね。どの方式で送ればいいかまだ分からないよ。"
	}
	if err := s.SendAirconIR(ctx, cmd); err != nil {
		s.logger.Printf("session=%s aircon_send_failed command=%+v error=%v", s.id, cmd, err)
		return true, "エアコン操作を送ろうとしたけど、赤外線送信でエラーになったよ。"
	}
	s.stateMu.Lock()
	s.lastIRProtocol = cmd.Protocol
	s.lastAircon = cmd
	s.saveIRStateLocked()
	s.stateMu.Unlock()
	return true, AirconCommandSpeech(cmd)
}

func (s *Session) SendAirconIR(ctx context.Context, cmd AirconCommand) error {
	raw, frequency, err := s.client.GenerateAirconIR(ctx, cmd)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return fmt.Errorf("IR API returned empty raw timings")
	}
	timings := make([]string, 0, len(raw))
	for _, value := range raw {
		timings = append(timings, strconv.Itoa(value))
	}
	if frequency == 0 {
		frequency = DefaultIRCarrierHz
	}
	_, err = s.CallMCP(map[string]any{
		"method": "tools/call",
		"params": map[string]any{
			"name": "self.robot.send_ir_raw",
			"arguments": map[string]any{
				"timings_usec": strings.Join(timings, ","),
				"carrier_hz":   frequency,
			},
		},
	}, 20*time.Second)
	return err
}

func AirconCommandSpeech(cmd AirconCommand) string {
	if !cmd.Power {
		return "エアコンをオフにしたよ。"
	}
	mode := localizedIRMode(cmd.Mode)
	switch cmd.Reason {
	case "temperature_up":
		return "温度を" + strconv.Itoa(cmd.Temp) + "度に上げたよ。"
	case "temperature_down":
		return "温度を" + strconv.Itoa(cmd.Temp) + "度に下げたよ。"
	}
	if cmd.Temp > 0 && cmd.Mode != "fan" {
		return mode + "を" + strconv.Itoa(cmd.Temp) + "度にしたよ。"
	}
	return mode + "にしたよ。"
}

func commandFromDecoded(protocol string, decoded map[string]any) AirconCommand {
	cmd := DefaultAirconCommand(protocol)
	if power, ok := decoded["power"]; ok {
		cmd.Power = boolValue(power)
	}
	if mode := strings.TrimSpace(toString(decoded["mode"])); mode != "" {
		cmd.Mode = mode
	}
	if tempText := strings.TrimSpace(toString(decoded["temperatureC"])); tempText != "" {
		if temp, err := strconv.Atoi(tempText); err == nil {
			cmd.Temp = clampTemp(temp)
		}
	}
	if fan := strings.TrimSpace(toString(decoded["fan"])); fan != "" {
		cmd.Fan = fan
	}
	return cmd
}

func clampTemp(temp int) int {
	if temp < 16 {
		return 16
	}
	if temp > 31 {
		return 31
	}
	return temp
}
