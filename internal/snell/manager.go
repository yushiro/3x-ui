package snell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

const stableRunInterval = time.Minute

const (
	snellExitDiagnosticCap       = snellProcessOutputVisibleCap
	snellExitDiagnosticHeadChars = 192
)

// Backoff bounds restart attempts after an unexpected sidecar exit.
type Backoff interface {
	Next() time.Duration
	Reset()
}

type exponentialBackoff struct{ index int }

var backoffDelays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, time.Minute}

// NewBackoff returns the bounded restart sequence 1s through 60s.
func NewBackoff() Backoff { return &exponentialBackoff{} }

func (b *exponentialBackoff) Next() time.Duration {
	index := min(b.index, len(backoffDelays)-1)
	delay := backoffDelays[index]
	if b.index < len(backoffDelays)-1 {
		b.index++
	}
	return delay
}

func (b *exponentialBackoff) Reset() { b.index = 0 }

// Status is the sidecar state exposed to runtime consumers.
type Status struct {
	Running   bool
	LastError string
	RestartAt time.Time
}

type entry struct {
	process     ManagedProcess
	instance    Instance
	status      Status
	backoff     Backoff
	intentional bool
	startedAt   time.Time
	generation  uint64
}

// Manager owns exactly one Snell sidecar per inbound ID.
type Manager struct {
	Launch     ProcessLauncher
	Nft        *NftManager
	BinaryPath string
	ConfigDir  string
	Host       HostChecker

	mu   sync.Mutex
	byID map[int]*entry

	lifecycleMu sync.Mutex
	lifecycles  map[int]*sync.Mutex

	now      func() time.Time
	schedule func(time.Duration, func())
	swept    bool
}

// NewManager constructs an independently testable sidecar manager.
func NewManager(launch ProcessLauncher, host HostChecker, nft *NftManager, binary, configDir string) *Manager {
	return &Manager{
		Launch:     launch,
		Host:       host,
		Nft:        nft,
		BinaryPath: binary,
		ConfigDir:  configDir,
		byID:       make(map[int]*entry),
		lifecycles: make(map[int]*sync.Mutex),
		now:        time.Now,
		schedule: func(delay time.Duration, fn func()) {
			time.AfterFunc(delay, fn)
		},
	}
}

type lifecycleContextKey struct{}

type lifecycleToken struct {
	manager *Manager
	id      int
}

// BeginLifecycle serializes a complete local sidecar transition for one
// inbound. The returned context lets nested Manager calls reuse the same
// boundary instead of self-deadlocking.
func (m *Manager) BeginLifecycle(ctx context.Context, id int) (context.Context, func(), error) {
	if m == nil || id <= 0 {
		return ctx, nil, errors.New("invalid Snell inbound id")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if token, ok := ctx.Value(lifecycleContextKey{}).(lifecycleToken); ok && token.manager == m && token.id == id {
		return ctx, func() {}, nil
	}
	m.lifecycleMu.Lock()
	lock := m.lifecycles[id]
	if lock == nil {
		lock = &sync.Mutex{}
		m.lifecycles[id] = lock
	}
	m.lifecycleMu.Unlock()
	lock.Lock()
	return context.WithValue(ctx, lifecycleContextKey{}, lifecycleToken{manager: m, id: id}), lock.Unlock, nil
}

func (m *Manager) withLifecycle(ctx context.Context, id int, fn func(context.Context) error) error {
	ctx, release, err := m.BeginLifecycle(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	return fn(ctx)
}

// Ensure starts the desired sidecar when it is enabled and below quota. A
// changed active instance is stopped before its final absolute counters seed
// the replacement's nft rules.
func (m *Manager) Ensure(ctx context.Context, instance Instance) error {
	return m.withLifecycle(ctx, instance.ID, func(ctx context.Context) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.ensureLocked(ctx, instance)
	})
}

func (m *Manager) ensureLocked(ctx context.Context, instance Instance) error {
	if instance.Listen == "" {
		instance.Listen = "0.0.0.0"
	}
	if _, err := RenderConfig(instance); err != nil {
		return err
	}
	if !instanceShouldRun(instance) {
		return m.stopLocked(ctx, instance.ID, true)
	}
	if m.Launch == nil || m.Host == nil || m.Nft == nil {
		return errors.New("Snell runtime dependencies are unavailable")
	}

	m.sweepOrphansLocked()
	cur := m.byID[instance.ID]
	replacePortRules := cur != nil && cur.instance.Port != instance.Port
	if cur != nil && cur.process != nil && cur.process.Running() {
		if sameProcessConfig(cur.instance, instance) {
			cur.instance = instance
			cur.status = Status{Running: true}
			return nil
		}
		if err := m.stopLocked(ctx, instance.ID, true); err != nil {
			return err
		}
		counters, err := m.Nft.Read(ctx, instance.ID)
		if err != nil {
			return fmt.Errorf("read final Snell counters: %w", err)
		}
		instance.Up = max(instance.Up, counters.UpBytes)
		instance.Down = max(instance.Down, counters.DownBytes)
		cur = m.byID[instance.ID]
	}

	if err := m.Host.Check(ctx); err != nil {
		return err
	}
	data, err := RenderConfig(instance)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.ConfigDir, 0o700); err != nil {
		return err
	}
	if replacePortRules {
		if err := m.Nft.RemoveInbound(ctx, instance.ID); err != nil {
			return err
		}
	}
	path := m.configPath(instance.ID)
	if err := WriteConfig(path, data); err != nil {
		return err
	}
	if err := m.Nft.EnsureInbound(ctx, instance.ID, instance.Port, instance.Up, instance.Down); err != nil {
		return err
	}
	process, err := m.Launch.Start(ctx, m.BinaryPath, path)
	if err != nil {
		return err
	}
	if cur == nil {
		cur = &entry{backoff: NewBackoff()}
		m.byID[instance.ID] = cur
	}
	if cur.backoff == nil {
		cur.backoff = NewBackoff()
	}
	cur.generation++
	cur.process = process
	cur.instance = instance
	cur.intentional = false
	cur.startedAt = m.now()
	cur.status = Status{Running: true}
	generation := cur.generation
	go m.waitForExit(instance.ID, generation, process)
	return nil
}

// Stop stops one managed process. Intentional stops, including disable and
// quota actions, never schedule a restart after their Wait returns.
func (m *Manager) Stop(ctx context.Context, id int, intentional bool) error {
	return m.withLifecycle(ctx, id, func(ctx context.Context) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.stopLocked(ctx, id, intentional)
	})
}

func (m *Manager) stopLocked(ctx context.Context, id int, intentional bool) error {
	cur := m.byID[id]
	if cur == nil {
		return nil
	}
	cur.generation++
	if intentional {
		cur.intentional = true
	}
	cur.status.Running = false
	cur.status.RestartAt = time.Time{}
	process := cur.process
	cur.process = nil
	if process == nil {
		return nil
	}
	return process.Stop(ctx)
}

// Remove stops the sidecar and removes only its owned config and nft objects.
func (m *Manager) Remove(ctx context.Context, id int) error {
	return m.withLifecycle(ctx, id, func(ctx context.Context) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		if err := m.stopLocked(ctx, id, true); err != nil {
			return err
		}
		if m.Nft != nil {
			if err := m.Nft.RemoveInbound(ctx, id); err != nil {
				return err
			}
		}
		delete(m.byID, id)
		if err := os.Remove(m.configPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
}

// Reconcile stops no-longer-desired sidecars then converges the desired set.
func (m *Manager) Reconcile(ctx context.Context, desired []Instance) error {
	want := make(map[int]bool, len(desired))
	for _, instance := range desired {
		want[instance.ID] = true
	}
	m.mu.Lock()
	stale := make([]int, 0)
	for id := range m.byID {
		if !want[id] {
			stale = append(stale, id)
		}
	}
	m.mu.Unlock()
	for _, id := range stale {
		if err := m.Remove(ctx, id); err != nil {
			return err
		}
	}
	for _, instance := range desired {
		if err := m.withLifecycle(ctx, instance.ID, func(ctx context.Context) error {
			m.mu.Lock()
			defer m.mu.Unlock()
			// nft counters are absolute and must never regress below their database
			// seed. A missing or lower counter means the process is stopped first;
			// Ensure then recreates named counters from the persisted values.
			cur := m.byID[instance.ID]
			if cur != nil && cur.process != nil && cur.process.Running() && instanceShouldRun(instance) && m.Nft != nil {
				counters, err := m.Nft.Read(ctx, instance.ID)
				if err != nil || counters.UpBytes < instance.Up || counters.DownBytes < instance.Down {
					if err := m.stopLocked(ctx, instance.ID, true); err != nil {
						return err
					}
				}
			}
			return m.ensureLocked(ctx, instance)
		}); err != nil {
			return err
		}
	}
	return nil
}

// ReadTraffic returns the private absolute counters for one sidecar.
func (m *Manager) ReadTraffic(ctx context.Context, id int) (Counters, error) {
	var counters Counters
	err := m.withLifecycle(ctx, id, func(ctx context.Context) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.Nft == nil {
			return errors.New("Snell nft manager is unavailable")
		}
		var err error
		counters, err = m.Nft.Read(ctx, id)
		return err
	})
	return counters, err
}

// HandleExit records an unexpected exit and schedules the bounded restart.
func (m *Manager) HandleExit(ctx context.Context, id int, err error) {
	m.mu.Lock()
	cur := m.byID[id]
	if cur == nil || (cur.process != nil && cur.process.Running()) {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	m.handleExit(ctx, id, nil, err)
}

func (m *Manager) waitForExit(id int, generation uint64, process ManagedProcess) {
	err := process.Wait()
	m.mu.Lock()
	cur := m.byID[id]
	if cur == nil || cur.generation != generation || cur.process != process {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	m.handleExit(context.Background(), id, process, err)
}

func (m *Manager) handleExit(_ context.Context, id int, process ManagedProcess, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.byID[id]
	if cur == nil || (process != nil && cur.process != process) {
		return
	}
	cur.process = nil
	cur.status.Running = false
	if cur.intentional || !instanceShouldRun(cur.instance) {
		cur.status.RestartAt = time.Time{}
		return
	}
	if cur.backoff == nil {
		cur.backoff = NewBackoff()
	}
	if m.now().Sub(cur.startedAt) >= stableRunInterval {
		cur.backoff.Reset()
	}
	if err == nil {
		err = errors.New("Snell sidecar exited")
	}
	cur.status.LastError = snellExitDiagnostic(err, snellProcessOutput(process), cur.instance.PSK)
	delay := cur.backoff.Next()
	cur.status.RestartAt = m.now().Add(delay)
	cur.generation++
	generation := cur.generation
	instance := cur.instance
	logger.Errorf("snell: runtime exited unexpectedly: inbound=%d error=%s", id, cur.status.LastError)
	m.schedule(delay, func() { m.restartAfterBackoff(id, generation, instance) })
}

func snellProcessOutput(process ManagedProcess) string {
	type processOutput interface{ LastOutput() string }
	if process == nil {
		return ""
	}
	out, ok := process.(processOutput)
	if !ok {
		return ""
	}
	return strings.TrimSpace(out.LastOutput())
}

func snellExitDiagnostic(err error, processOutput, psk string) string {
	if err == nil {
		err = errors.New("Snell sidecar exited")
	}
	diagnostic := err.Error()
	if processOutput != "" {
		diagnostic = diagnostic + ": " + processOutput
	}
	return sanitizeAndTrimSnellExitDiagnostic(diagnostic, psk)
}

func sanitizeAndTrimSnellExitDiagnostic(message, psk string) string {
	message = sanitizeSnellExitMessage(message, psk)
	if len(message) <= snellExitDiagnosticCap {
		return strings.TrimSpace(message)
	}
	head := snellExitDiagnosticHeadChars
	if head > len(message) {
		head = len(message)
	}
	tailBudget := snellExitDiagnosticCap - head - 4
	if tailBudget <= 0 {
		return strings.TrimSpace(message[:snellExitDiagnosticCap])
	}
	return strings.TrimSpace(message[:head] + "... " + message[len(message)-tailBudget:])
}

func sanitizeSnellExitMessage(message, psk string) string {
	if psk == "" {
		return strings.TrimSpace(message)
	}
	secret := strings.TrimSpace(psk)
	message = strings.ReplaceAll(message, secret, "[redacted]")
	return strings.TrimSpace(strings.ReplaceAll(message, strconv.Quote(psk), "[redacted]"))
}

func (m *Manager) restartAfterBackoff(id int, generation uint64, instance Instance) {
	m.mu.Lock()
	cur := m.byID[id]
	if cur == nil || cur.generation != generation || cur.intentional || !instanceShouldRun(cur.instance) {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	_ = m.Ensure(context.Background(), instance)
}

// Status returns a snapshot of one sidecar's status.
func (m *Manager) Status(id int) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur := m.byID[id]; cur != nil {
		return cur.status
	}
	return Status{}
}

// CheckHost validates the fixed Host-only prerequisites without starting a
// sidecar. CRUD paths use it before committing an enabled transition.
func (m *Manager) CheckHost(ctx context.Context) error {
	if m == nil || m.Host == nil {
		return errors.New("Snell host checker is unavailable")
	}
	return m.Host.Check(ctx)
}

// ResetTraffic resets the private nft counters for one managed inbound.
func (m *Manager) ResetTraffic(ctx context.Context, id int) error {
	return m.withLifecycle(ctx, id, func(ctx context.Context) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.Nft == nil {
			return errors.New("Snell nft manager is unavailable")
		}
		if err := m.stopLocked(ctx, id, true); err != nil {
			return err
		}
		if err := m.Nft.ResetInbound(ctx, id); err != nil {
			return err
		}
		if cur := m.byID[id]; cur != nil {
			cur.instance.Up = 0
			cur.instance.Down = 0
		}
		return nil
	})
}

func (m *Manager) configPath(id int) string {
	return filepath.Join(m.ConfigDir, fmt.Sprintf("snell-%d.conf", id))
}

func (m *Manager) sweepOrphansLocked() {
	if m.swept {
		return
	}
	m.swept = true
	cleanupOwnedOrphans(m.BinaryPath, m.ConfigDir)
}

func instanceShouldRun(instance Instance) bool {
	if !instance.Enable {
		return false
	}
	if instance.Total <= 0 {
		return true
	}
	return instance.Up < instance.Total && instance.Down < instance.Total-instance.Up
}

func sameProcessConfig(a, b Instance) bool {
	return a.ID == b.ID && a.Listen == b.Listen && a.Port == b.Port && a.PSK == b.PSK && a.Enable == b.Enable
}
