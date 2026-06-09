package main

import "testing"

func TestIsSupportedACResultRejectsNonACMultibrackets(t *testing.T) {
	if isSupportedACResult(map[string]any{"manufacturer": "MULTIBRACKETS", "protocol": "MULTIBRACKETS"}, map[string]any{}) {
		t.Fatal("MULTIBRACKETS must not be treated as a supported AC result")
	}
}

func TestIsSupportedACResultAcceptsKnownACProtocol(t *testing.T) {
	if !isSupportedACResult(map[string]any{"manufacturer": "Unknown", "protocol": "PANASONIC_AC"}, map[string]any{}) {
		t.Fatal("known AC protocol prefix should be supported")
	}
}
