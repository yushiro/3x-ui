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
	for _, want := range []string{"[snell-server]", `listen = 0.0.0.0:443`, `psk = "0123456789abcdef"`, "ipv6 = false"} {
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

func TestConfigRenderUsesBoundedListenHostPort(t *testing.T) {
	testCases := []struct {
		name string
		in   Instance
		want string
	}{
		{name: "ipv4", in: Instance{ID: 8, Listen: "10.0.0.1", Port: 8443, PSK: "0123456789abcdef"}, want: `[snell-server]
listen = 10.0.0.1:8443`},
		{name: "ipv6", in: Instance{ID: 9, Listen: "::1", Port: 8443, PSK: "0123456789abcdef"}, want: `[snell-server]
listen = [::1]:8443`},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RenderConfig(tc.in)
			if err != nil {
				t.Fatalf("RenderConfig: %v", err)
			}
			if !strings.Contains(string(got), tc.want) {
				t.Fatalf("unexpected config: %s", string(got))
			}
		})
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
