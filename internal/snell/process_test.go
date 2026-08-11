package snell

import (
	"strconv"
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

func TestBoundedProcessOutputRetainsSplitPSKForSanitization(t *testing.T) {
	payload := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	const splitOffset = 32
	output := buildSplitOutputPayload(t, payload, splitOffset)

	old := newBoundedOutput(snellProcessOutputVisibleCap)
	if _, err := old.Write([]byte(output)); err != nil {
		t.Fatalf("write: %v", err)
	}
	oldTail := old.String()
	if strings.Contains(oldTail, payload[:splitOffset]) {
		t.Fatalf("old capture unexpectedly kept full raw secret prefix: %q", oldTail)
	}
	if !strings.Contains(oldTail, payload[splitOffset:]) {
		t.Fatalf("old capture dropped all of split secret: %q", oldTail)
	}

	capture := newBoundedOutput(snellProcessOutputCaptureCap)
	if _, err := capture.Write([]byte(output)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(capture.String(), payload) {
		t.Fatalf("expanded capture dropped split raw secret: %q", capture.String())
	}
}

func TestBoundedProcessOutputRetainsSplitQuotedPSKForSanitization(t *testing.T) {
	secret := "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ+/"
	quoted := strconv.Quote(secret)
	output := buildSplitOutputPayload(t, quoted, 16)
	old := newBoundedOutput(snellProcessOutputVisibleCap)
	if _, err := old.Write([]byte(output)); err != nil {
		t.Fatalf("write: %v", err)
	}
	oldTail := old.String()
	if strings.Contains(oldTail, quoted[:16]) {
		t.Fatalf("old capture unexpectedly kept full quoted prefix: %q", oldTail)
	}
	if !strings.Contains(oldTail, quoted[16:]) {
		t.Fatalf("old capture dropped all of split quoted secret: %q", oldTail)
	}

	capture := newBoundedOutput(snellProcessOutputCaptureCap)
	if _, err := capture.Write([]byte(output)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(capture.String(), quoted) {
		t.Fatalf("expanded capture dropped split quoted secret: %q", capture.String())
	}
}

func buildSplitOutputPayload(t *testing.T, payload string, splitOffset int) string {
	t.Helper()
	const (
		boundary = 8000
		prefix   = "psk = "
	)
	if splitOffset <= 0 || splitOffset >= len(payload) {
		t.Fatalf("invalid split offset %d for payload len %d", splitOffset, len(payload))
	}
	line := prefix + payload
	suffixLen := snellProcessOutputVisibleCap + splitOffset - len(payload)
	if suffixLen < 0 {
		t.Fatalf("invalid test data: line=%d split=%d", len(line), splitOffset)
	}
	prefixLen := boundary - len(prefix) - splitOffset
	if prefixLen < 0 {
		t.Fatalf("invalid boundary setup: %d", prefixLen)
	}
	return strings.Repeat("x", prefixLen) + line + strings.Repeat("x", suffixLen)
}
