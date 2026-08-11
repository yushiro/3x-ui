package snell

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeManagedProcess struct {
	mu      sync.Mutex
	running bool
	stops   int
	wait    chan error
	output  string
}

func newFakeManagedProcess() *fakeManagedProcess {
	return &fakeManagedProcess{running: true, wait: make(chan error, 1)}
}

func (p *fakeManagedProcess) Stop(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stops++
	p.running = false
	return nil
}

func (p *fakeManagedProcess) Wait() error { return <-p.wait }

func (p *fakeManagedProcess) Running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

func (p *fakeManagedProcess) markExited() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.running = false
}

func (p *fakeManagedProcess) LastOutput() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.output
}

type fakeLauncher struct {
	starts []struct{ binary, config string }
	procs  []*fakeManagedProcess
}

func (f *fakeLauncher) Start(_ context.Context, binary, config string) (ManagedProcess, error) {
	f.starts = append(f.starts, struct{ binary, config string }{binary, config})
	p := newFakeManagedProcess()
	f.procs = append(f.procs, p)
	return p, nil
}

type fakeHost struct{ err error }

func (f fakeHost) Check(context.Context) error { return f.err }

type fakeNftExecutor struct {
	calls  [][]string
	output []byte
}

func (f *fakeNftExecutor) Run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	return f.output, nil
}

func testNft(output string) (*NftManager, *fakeNftExecutor) {
	f := &fakeNftExecutor{output: []byte(output)}
	return &NftManager{Exec: f}, f
}

func testInstance(id int) Instance {
	return Instance{ID: id, Listen: "0.0.0.0", Port: 443, PSK: "valid-psk-12345678", Enable: true, Total: 1000}
}

func TestManagerStartsOneOwnedProcess(t *testing.T) {
	launch := &fakeLauncher{}
	nft, _ := testNft(`{"nftables":[]}`)
	m := NewManager(launch, fakeHost{}, nft, "/bin/snell-server", t.TempDir())
	if err := m.Ensure(context.Background(), testInstance(3)); err != nil {
		t.Fatal(err)
	}
	if err := m.Ensure(context.Background(), testInstance(3)); err != nil {
		t.Fatal(err)
	}
	if len(launch.starts) != 1 || launch.starts[0].binary != "/bin/snell-server" || !strings.HasSuffix(launch.starts[0].config, "snell-3.conf") {
		t.Fatalf("bad launch: %#v", launch.starts)
	}
	if status := m.Status(3); !status.Running || status.LastError != "" {
		t.Fatalf("bad status: %+v", status)
	}
}

func TestManagerUpdateStopsAndSuppressesIntentionalExit(t *testing.T) {
	launch := &fakeLauncher{}
	nft, _ := testNft(`{"nftables":[{"counter":{"name":"snell_3_up","bytes":150}},{"counter":{"name":"snell_3_down","bytes":250}}]}`)
	m := NewManager(launch, fakeHost{}, nft, "/bin/snell-server", t.TempDir())
	if err := m.Ensure(context.Background(), testInstance(3)); err != nil {
		t.Fatal(err)
	}
	first := launch.procs[0]
	updated := testInstance(3)
	updated.Port = 8443
	if err := m.Ensure(context.Background(), updated); err != nil {
		t.Fatal(err)
	}
	if first.stops != 1 || len(launch.starts) != 2 {
		t.Fatalf("update did not replace active process: stops=%d starts=%d", first.stops, len(launch.starts))
	}
	m.handleExit(context.Background(), 3, first, errors.New("old process exited"))
	if got := m.Status(3); !got.RestartAt.IsZero() {
		t.Fatalf("stale intentional exit scheduled restart: %+v", got)
	}
}

func TestManagerQuotaStopAndCrashBackoff(t *testing.T) {
	launch := &fakeLauncher{}
	nft, _ := testNft(`{"nftables":[]}`)
	m := NewManager(launch, fakeHost{}, nft, "/bin/snell-server", t.TempDir())
	base := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return base }
	m.schedule = func(time.Duration, func()) {}
	if err := m.Ensure(context.Background(), testInstance(4)); err != nil {
		t.Fatal(err)
	}
	launch.procs[0].markExited()
	m.HandleExit(context.Background(), 4, errors.New("crash"))
	if got := m.Status(4); !got.RestartAt.Equal(base.Add(time.Second)) {
		t.Fatalf("first restart = %v, want %v", got.RestartAt, base.Add(time.Second))
	}

	quota := testInstance(5)
	quota.Up, quota.Total = 100, 100
	if err := m.Ensure(context.Background(), quota); err != nil {
		t.Fatal(err)
	}
	if len(launch.starts) != 1 || m.Status(5).Running {
		t.Fatalf("quota-depleted inbound started: starts=%d status=%+v", len(launch.starts), m.Status(5))
	}
}

func TestManagerRedactsProcessOutputFromLastError(t *testing.T) {
	launch := &fakeLauncher{}
	nft, _ := testNft(`{"nftables":[]}`)
	m := NewManager(launch, fakeHost{}, nft, "/bin/snell-server", t.TempDir())
	instance := testInstance(9)
	process := newFakeManagedProcess()
	process.output = "config check failed: psk = \"" + instance.PSK + "\""
	m.withLifecycle(context.Background(), instance.ID, func(context.Context) error {
		m.mu.Lock()
		m.byID[instance.ID] = &entry{
			process:     process,
			instance:    instance,
			backoff:     NewBackoff(),
			startedAt:   m.now(),
			generation:  1,
			intentional: false,
		}
		m.mu.Unlock()
		return nil
	})
	m.handleExit(context.Background(), instance.ID, process, errors.New("exit status 1"))
	status := m.Status(instance.ID)
	if strings.Contains(status.LastError, instance.PSK) {
		t.Fatalf("secret leaked in LastError: %q", status.LastError)
	}
	if status.LastError == "" || !strings.Contains(status.LastError, "exit status 1") {
		t.Fatalf("unexpected LastError: %q", status.LastError)
	}
}

func TestManagerRedactsBoundarySplitRawAndQuotedPSKFromExitDiagnostics(t *testing.T) {
	tests := []struct {
		name        string
		psk         string
		payload     string
		splitOffset int
	}{
		{
			name:        "raw",
			psk:         "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/",
			payload:     "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/",
			splitOffset: 32,
		},
		{
			name:        "quoted",
			psk:         "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ+/",
			payload:     strconv.Quote("0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ+/"),
			splitOffset: 16,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			output := buildBoundarySplitOutputForDiagnosticTest(t, tc.payload, tc.splitOffset)
			old := newBoundedOutput(snellProcessOutputVisibleCap)
			if _, err := old.Write([]byte(output)); err != nil {
				t.Fatalf("write old capture: %v", err)
			}
			oldTail := old.String()
			if strings.Contains(oldTail, tc.payload[:tc.splitOffset]) {
				t.Fatalf("old capture unexpectedly retained full payload prefix: %q", oldTail)
			}
			if !strings.Contains(oldTail, tc.payload[tc.splitOffset:]) {
				t.Fatalf("old capture dropped all of split payload: %q", oldTail)
			}

			capture := newBoundedOutput(snellProcessOutputCaptureCap)
			if _, err := capture.Write([]byte(output)); err != nil {
				t.Fatalf("write new capture: %v", err)
			}
			if !strings.Contains(capture.String(), tc.payload) {
				t.Fatalf("expanded capture did not retain full payload: %q", capture.String())
			}
			last := snellExitDiagnostic(errors.New("exit status 1"), capture.String(), tc.psk)
			if strings.Contains(last, tc.psk) {
				t.Fatalf("raw psk leaked in LastError: %q", last)
			}
			if strings.Contains(last, tc.payload[tc.splitOffset:]) {
				t.Fatalf("split payload fragment leaked in LastError: %q", last)
			}
			if tc.name == "quoted" {
				quoted := strconv.Quote(tc.psk)
				if strings.Contains(last, quoted) {
					t.Fatalf("quoted psk leaked in LastError: %q", last)
				}
				if strings.Contains(last, quoted[tc.splitOffset:]) {
					t.Fatalf("quoted fragment leaked in LastError: %q", last)
				}
			}
			if !strings.Contains(last, "exit status 1") {
				t.Fatalf("exit error prefix missing: %q", last)
			}
		})
	}
}

func buildBoundarySplitOutputForDiagnosticTest(t *testing.T, payload string, splitOffset int) string {
	t.Helper()
	const (
		boundary           = 8000
		visibleBufferLimit = snellProcessOutputVisibleCap
		payloadPrefix      = "psk = "
	)
	if splitOffset <= 0 || splitOffset >= len(payload) {
		t.Fatalf("invalid split offset %d for payload len %d", splitOffset, len(payload))
	}
	line := payloadPrefix + payload
	suffixLen := visibleBufferLimit + splitOffset - len(payload)
	if suffixLen < 0 {
		t.Fatalf("invalid test payload: payload=%d split=%d", len(payload), splitOffset)
	}
	prefixLen := boundary - len(payloadPrefix) - splitOffset
	if prefixLen < 0 {
		t.Fatalf("invalid boundary setup: %d", prefixLen)
	}
	return strings.Repeat("x", prefixLen) + line + strings.Repeat("x", suffixLen)
}

func TestManagerReconcileRepairsLowerCountersByStoppingBeforeRestart(t *testing.T) {
	launch := &fakeLauncher{}
	nft, exec := testNft(`{"nftables":[]}`)
	m := NewManager(launch, fakeHost{}, nft, "/bin/snell-server", t.TempDir())
	instance := testInstance(8)
	instance.Up, instance.Down = 100, 200
	if err := m.Ensure(context.Background(), instance); err != nil {
		t.Fatal(err)
	}
	exec.output = []byte(`{"nftables":[{"counter":{"name":"snell_8_up","bytes":50}},{"counter":{"name":"snell_8_down","bytes":150}}]}`)
	if err := m.Reconcile(context.Background(), []Instance{instance}); err != nil {
		t.Fatal(err)
	}
	if launch.procs[0].stops != 1 || len(launch.starts) != 2 {
		t.Fatalf("lower counters were not repaired by stop/restart: stops=%d starts=%d", launch.procs[0].stops, len(launch.starts))
	}
}

func TestManagerResetsOnlyInboundCounters(t *testing.T) {
	launch := &fakeLauncher{}
	nft, exec := testNft(`{"nftables":[]}`)
	m := NewManager(launch, fakeHost{}, nft, "/bin/snell-server", t.TempDir())
	if err := m.ResetTraffic(context.Background(), 9); err != nil {
		t.Fatal(err)
	}
	if len(exec.calls) != 2 || strings.Join(exec.calls[0], " ") != "reset counter inet xui_snell snell_9_up" || strings.Join(exec.calls[1], " ") != "reset counter inet xui_snell snell_9_down" {
		t.Fatalf("unexpected reset calls: %#v", exec.calls)
	}
}

func TestManagerBackoffIsBoundedAndResettable(t *testing.T) {
	backoff := NewBackoff()
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, time.Minute, time.Minute}
	for i, delay := range want {
		if got := backoff.Next(); got != delay {
			t.Fatalf("delay %d = %s, want %s", i, got, delay)
		}
	}
	backoff.Reset()
	if got := backoff.Next(); got != time.Second {
		t.Fatalf("reset delay = %s, want 1s", got)
	}
}

func TestManagerRejectsHostFailureBeforeLaunch(t *testing.T) {
	launch := &fakeLauncher{}
	nft, _ := testNft(`{"nftables":[]}`)
	m := NewManager(launch, fakeHost{err: errors.New("unsupported host")}, nft, "/bin/snell-server", t.TempDir())
	if err := m.Ensure(context.Background(), testInstance(6)); err == nil {
		t.Fatal("host failure was accepted")
	}
	if len(launch.starts) != 0 {
		t.Fatal("host failure launched a process")
	}
}
