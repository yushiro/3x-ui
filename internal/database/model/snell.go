package model

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"unicode"
)

// SnellSettings is the complete settings contract for a Snell inbound. Snell
// uses one inbound-level pre-shared key; it does not have panel client models.
type SnellSettings struct {
	PSK string `json:"psk"`
}

var errInvalidSnellSettings = errors.New("invalid Snell settings")

// ParseSnellSettings parses and validates the settings persisted for a Snell
// inbound. It deliberately accepts no fields besides psk, keeping clients and
// other Xray-specific settings out of Snell inbounds.
func ParseSnellSettings(raw string) (SnellSettings, error) {
	psk, present, err := decodeSnellSettings(raw)
	if err != nil || !present {
		return SnellSettings{}, errInvalidSnellSettings
	}
	settings := SnellSettings{PSK: psk}
	if err := ValidateSnellSettings(settings); err != nil {
		return SnellSettings{}, err
	}
	return settings, nil
}

// ValidateSnellSettings validates an inbound-level Snell PSK without putting
// the secret into an error message.
func ValidateSnellSettings(settings SnellSettings) error {
	if len(settings.PSK) < 16 || len(settings.PSK) > 128 {
		return errors.New("invalid Snell PSK")
	}
	for _, r := range settings.PSK {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return errors.New("invalid Snell PSK")
		}
	}
	return nil
}

// NewSnellPSK returns a cryptographically random 32-byte PSK encoded as
// lowercase hexadecimal.
func NewSnellPSK() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// NormalizeSnellSettings canonicalizes settings for either a new Snell
// inbound or an edit. An empty submitted PSK preserves an existing one, or
// generates a new key for creates.
func NormalizeSnellSettings(raw string, existing *SnellSettings) (string, error) {
	psk := ""
	if raw != "" {
		var present bool
		var err error
		psk, present, err = decodeSnellSettings(raw)
		if err != nil {
			return "", errInvalidSnellSettings
		}
		if !present {
			psk = ""
		}
	}

	if psk == "" && existing != nil {
		psk = existing.PSK
	}
	if psk == "" {
		var err error
		psk, err = NewSnellPSK()
		if err != nil {
			return "", err
		}
	}

	settings := SnellSettings{PSK: psk}
	if err := ValidateSnellSettings(settings); err != nil {
		return "", err
	}
	b, err := json.Marshal(settings)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// decodeSnellSettings accepts the editing shape (where psk can be omitted),
// while rejecting every other field. It returns whether psk was present.
func decodeSnellSettings(raw string) (string, bool, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil || fields == nil {
		return "", false, errInvalidSnellSettings
	}
	for key := range fields {
		if key != "psk" {
			return "", false, errInvalidSnellSettings
		}
	}
	value, ok := fields["psk"]
	if !ok {
		return "", false, nil
	}
	var psk string
	if err := json.Unmarshal(value, &psk); err != nil {
		return "", false, errInvalidSnellSettings
	}
	return psk, true, nil
}
