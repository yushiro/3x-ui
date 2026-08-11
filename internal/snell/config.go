package snell

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// RenderConfig renders the local Snell v5 configuration for one instance.
func RenderConfig(instance Instance) ([]byte, error) {
	if err := validateInstance(instance); err != nil {
		return nil, err
	}
	listen := net.JoinHostPort(instance.Listen, strconv.Itoa(instance.Port))
	return []byte(fmt.Sprintf("[snell-server]\nlisten = %s\npsk = %s\nipv6 = false\n",
		listen, strconv.Quote(instance.PSK))), nil
}

func validateInstance(instance Instance) error {
	if instance.ID <= 0 {
		return fmt.Errorf("invalid Snell inbound id")
	}
	if instance.Port < 1 || instance.Port > 65535 {
		return fmt.Errorf("invalid Snell port")
	}
	if net.ParseIP(instance.Listen) == nil {
		return fmt.Errorf("invalid Snell listen address")
	}
	if err := model.ValidateSnellSettings(model.SnellSettings{PSK: instance.PSK}); err != nil {
		return fmt.Errorf("invalid Snell PSK")
	}
	return nil
}

// WriteConfig atomically writes a credential-bearing configuration with 0600
// permissions, including when replacing an existing file with wider mode.
func WriteConfig(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".snell-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if err != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if err = tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
