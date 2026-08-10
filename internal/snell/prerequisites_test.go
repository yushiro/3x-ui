package snell

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var errCommandExitOne = errors.New("exit status 1")

type fakeCommandRunner struct {
	calls   [][]string
	errs    map[string]error
	success map[string]bool
}

func (f *fakeCommandRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	key := strings.Join(append([]string{name}, args...), " ")
	if f.success[key] {
		return nil, nil
	}
	if err, ok := f.errs[key]; ok {
		return nil, err
	}
	return nil, errCommandExitOne
}

func executableForHostTest(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "snell-server")
	if err := os.WriteFile(path, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHostRejectsUnsupportedPlatformAndMissingBinary(t *testing.T) {
	runner := &fakeCommandRunner{}
	if err := CheckHost(context.Background(), runner, "missing", "darwin", "amd64"); err == nil {
		t.Fatal("non-Linux host was accepted")
	}
	if len(runner.calls) != 0 {
		t.Fatal("unsupported host must not run commands")
	}
	if err := CheckHost(context.Background(), runner, "missing", "linux", "mips64"); err == nil {
		t.Fatal("unsupported architecture was accepted")
	}
	if err := CheckHost(context.Background(), runner, "missing", "linux", "amd64"); err == nil {
		t.Fatal("missing binary was accepted")
	}
}

func TestHostRejectsDockerMissingNftAndPermissions(t *testing.T) {
	bin := executableForHostTest(t)
	cases := []struct {
		name    string
		errs    map[string]error
		success map[string]bool
	}{
		{"docker", map[string]error{}, map[string]bool{"test -f /.dockerenv": true}},
		{"missing nft", map[string]error{"nft --version": errors.New("not found")}, nil},
		{"nft permission", map[string]error{"nft list ruleset": errors.New("operation not permitted")}, map[string]bool{"nft --version": true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeCommandRunner{errs: tc.errs, success: tc.success}
			if err := CheckHost(context.Background(), runner, bin, "linux", "amd64"); err == nil {
				t.Fatal("unsafe host was accepted")
			}
		})
	}
}

func TestHostAcceptsSupportedBareLinuxHost(t *testing.T) {
	bin := executableForHostTest(t)
	runner := &fakeCommandRunner{errs: map[string]error{
		"test -f /.dockerenv": errCommandExitOne,
		"grep -Eq (docker|containerd|kubepods) /proc/1/cgroup": errCommandExitOne,
	}, success: map[string]bool{"nft --version": true, "nft list ruleset": true}}
	if err := CheckHost(context.Background(), runner, bin, "linux", "amd64"); err != nil {
		t.Fatalf("CheckHost: %v", err)
	}
}

func TestHostPackageHasNoDownloader(t *testing.T) {
	for _, name := range []string{"settings.go", "config.go", "prerequisites.go", "nftables.go"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, forbidden := range []string{"net/http", "http.Get", "Download"} {
			if strings.Contains(string(data), forbidden) {
				t.Fatalf("%s contains downloader marker %q", name, forbidden)
			}
		}
	}
}
