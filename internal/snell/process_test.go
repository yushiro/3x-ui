package snell

import (
	"strings"
	"testing"
)

func TestProcessUsesOnlyConfigArgument(t *testing.T) {
	args := processArgs("/opt/x-ui/bin/snell/snell-server", "/opt/x-ui/bin/snell/config/snell-7.conf")
	if len(args) != 2 || args[0] != "-c" || args[1] != "/opt/x-ui/bin/snell/config/snell-7.conf" {
		t.Fatalf("unsafe process args: %#v", args)
	}
}

func TestOrphanMatchesOnlyOwnedConfigArgv(t *testing.T) {
	binary := "/opt/x-ui/bin/snell/snell-server"
	dir := "/opt/x-ui/bin/snell/config"
	if !isOwnedProcessArgs(binary, dir, []string{binary, "-c", dir + "/snell-7.conf"}) {
		t.Fatal("owned Snell process was not recognized")
	}
	if !isOwnedProcessArgs(binary, dir, []string{binary, "--config", dir + "/snell-7.conf"}) {
		t.Fatal("legacy --config-owned process was not recognized for cleanup")
	}
	for _, args := range [][]string{
		{"/usr/bin/snell-server", "--config", dir + "/snell-7.conf"},
		{binary, "--config", "/tmp/snell-7.conf"},
		{binary, "--config", dir + "/other.conf"},
		{binary, "--other", dir + "/snell-7.conf"},
	} {
		if isOwnedProcessArgs(binary, dir, args) {
			t.Fatalf("unrelated process matched: %#v", args)
		}
	}
}

func TestBoundedProcessOutputSkipsOldestBytesOnOverflow(t *testing.T) {
	out := newBoundedOutput(16)
	if _, err := out.Write([]byte("0123456789")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got, want := out.String(), "0123456789"; got != want {
		t.Fatalf("captured output = %q, want %q", got, want)
	}
	if _, err := out.Write([]byte("abcdefghijklmnopqr")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(out.String()) > 16 {
		t.Fatalf("bounded output exceeded limit: len=%d", len(out.String()))
	}
	if !strings.HasSuffix(out.String(), "cdefghijklmnopqr") {
		t.Fatalf("unexpected bounded output: %q", out.String())
	}
}
