package model

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/go-playground/validator/v10"
)

func TestParseSnellSettingsRejectsInvalidInputWithoutLeakingPSK(t *testing.T) {
	const validPSK = "0123456789abcdef"
	cases := []struct {
		name string
		raw  string
	}{
		{name: "malformed JSON", raw: `{"psk":`},
		{name: "missing PSK", raw: `{}`},
		{name: "short PSK", raw: `{"psk":"short"}`},
		{name: "whitespace PSK", raw: `{"psk":"01234567 89abcdef"}`},
		{name: "control character PSK", raw: `{"psk":"01234567\n89abcdef"}`},
		{name: "client settings forbidden", raw: `{"psk":"` + validPSK + `","clients":[]}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseSnellSettings(tc.raw)
			if err == nil {
				t.Fatal("ParseSnellSettings accepted invalid input")
			}
			if strings.Contains(err.Error(), validPSK) {
				t.Fatalf("error leaked PSK: %q", err)
			}
		})
	}

	parsed, err := ParseSnellSettings(`{"psk":"` + validPSK + `"}`)
	if err != nil {
		t.Fatalf("ParseSnellSettings valid settings: %v", err)
	}
	if parsed.PSK != validPSK {
		t.Fatalf("PSK = %q, want %q", parsed.PSK, validPSK)
	}
}

func TestNewSnellPSKProducesDistinctLowercaseHexSecrets(t *testing.T) {
	first, err := NewSnellPSK()
	if err != nil {
		t.Fatalf("NewSnellPSK first: %v", err)
	}
	second, err := NewSnellPSK()
	if err != nil {
		t.Fatalf("NewSnellPSK second: %v", err)
	}
	if len(first) != 64 || len(second) != 64 {
		t.Fatalf("PSK lengths = %d, %d; want 64", len(first), len(second))
	}
	if first == second {
		t.Fatal("two generated PSKs must not be equal")
	}
	if _, err := hex.DecodeString(first); err != nil || strings.ToLower(first) != first {
		t.Fatalf("first PSK is not lowercase hex: %q", first)
	}
}

func TestNormalizeSnellSettingsCreateAndEdit(t *testing.T) {
	created, err := NormalizeSnellSettings("", nil)
	if err != nil {
		t.Fatalf("NormalizeSnellSettings create: %v", err)
	}
	createdSettings, err := ParseSnellSettings(created)
	if err != nil {
		t.Fatalf("created settings are invalid: %v", err)
	}
	if createdSettings.PSK == "" {
		t.Fatal("create must generate a PSK")
	}

	old := &SnellSettings{PSK: "old-valid-psk-1234"}
	preserved, err := NormalizeSnellSettings(`{"psk":""}`, old)
	if err != nil {
		t.Fatalf("NormalizeSnellSettings empty edit: %v", err)
	}
	if got, err := ParseSnellSettings(preserved); err != nil || got.PSK != old.PSK {
		t.Fatalf("empty edit must preserve the old PSK; got=%q err=%v", got.PSK, err)
	}

	replaced, err := NormalizeSnellSettings(`{"psk":"new-valid-psk-1234"}`, old)
	if err != nil {
		t.Fatalf("NormalizeSnellSettings replacement: %v", err)
	}
	if got, err := ParseSnellSettings(replaced); err != nil || got.PSK != "new-valid-psk-1234" {
		t.Fatalf("non-empty edit must replace the PSK; got=%q err=%v", got.PSK, err)
	}
}

func TestInboundProtocolValidatorAcceptsSnell(t *testing.T) {
	v := validator.New(validator.WithRequiredStructEnabled())
	if err := v.Struct(Inbound{Protocol: Snell, Port: 443}); err != nil {
		t.Fatalf("Snell protocol rejected: %v", err)
	}
	if err := v.Struct(Inbound{Protocol: Protocol("not-a-protocol"), Port: 443}); err == nil {
		t.Fatal("unknown protocol accepted")
	}
}
