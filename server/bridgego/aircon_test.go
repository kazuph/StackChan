package bridgego

import "testing"

func TestAirconCommandFromTextCoolWithTemperature(t *testing.T) {
	cmd, ok := AirconCommandFromText("冷房を25度にして", DefaultAirconCommand("DAIKIN"))
	if !ok {
		t.Fatal("expected aircon command")
	}
	if cmd.Protocol != "DAIKIN" || !cmd.Power || cmd.Mode != "cool" || cmd.Temp != 25 || cmd.Fan != "auto" {
		t.Fatalf("cmd=%+v", cmd)
	}
}

func TestAirconCommandFromTextHotRoomTurnsOnCooling(t *testing.T) {
	cmd, ok := AirconCommandFromText("部屋が暑いなぁ", DefaultAirconCommand("PANASONIC_AC"))
	if !ok {
		t.Fatal("expected aircon command")
	}
	if cmd.Mode != "cool" || !cmd.Power || cmd.Temp != 26 {
		t.Fatalf("cmd=%+v", cmd)
	}
}

func TestAirconCommandFromTextTemperatureUpUsesCurrentState(t *testing.T) {
	current := AirconCommand{Protocol: "HITACHI_AC424", Power: true, Mode: "heat", Temp: 23, Fan: "auto"}
	cmd, ok := AirconCommandFromText("温度上げて", current)
	if !ok {
		t.Fatal("expected aircon command")
	}
	if cmd.Mode != "heat" || cmd.Temp != 24 {
		t.Fatalf("cmd=%+v", cmd)
	}
}

func TestAirconCommandFromTextPowerOff(t *testing.T) {
	cmd, ok := AirconCommandFromText("エアコン消して", DefaultAirconCommand("DAIKIN"))
	if !ok {
		t.Fatal("expected aircon command")
	}
	if cmd.Power {
		t.Fatalf("cmd=%+v", cmd)
	}
}

func TestAirconCommandFromTextIgnoresNonAirconSpeech(t *testing.T) {
	if _, ok := AirconCommandFromText("今日の天気を教えて", DefaultAirconCommand("DAIKIN")); ok {
		t.Fatal("unexpected aircon command")
	}
}
