package snell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigRenderAndWriteArePrivate(t *testing.T) {
	data, err := RenderConfig(Instance{ID: 7, Listen: "0.0.0.0", Port: 443, PSK: "0123456789abcdef"})
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	text := string(data)
	for _, want := range []string{"[Snell Server]", `interface = "0.0.0.0"`, "port = 443", `psk = "0123456789abcdef"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("config missing %q: %s", want, text)
		}
	}

	path := filepath.Join(t.TempDir(), "snell.conf")
	if err := WriteConfig(path, data); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config permissions = %o, want 0600", got)
	}
}

func TestConfigRejectsUnsafeInstance(t *testing.T) {
	valid := Instance{ID: 1, Listen: "0.0.0.0", Port: 443, PSK: "0123456789abcdef"}
	for _, mutate := range []func(*Instance){
		func(i *Instance) { i.ID = 0 },
		func(i *Instance) { i.Port = 0 },
		func(i *Instance) { i.Listen = "0.0.0.0; bad" },
		func(i *Instance) { i.PSK = "short" },
	} {
		instance := valid
		mutate(&instance)
		if _, err := RenderConfig(instance); err == nil {
			t.Fatalf("RenderConfig accepted unsafe instance: %+v", instance)
		}
	}
}
