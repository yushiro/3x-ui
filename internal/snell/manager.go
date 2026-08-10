package snell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const stableRunInterval = time.Minute

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
		now:        time.Now,
		schedule: func(delay time.Duration, fn func()) {
			time.AfterFunc(delay, fn)
		},
	}
}

// Ensure starts the desired sidecar when it is enabled and below quota. A
// changed active instance is stopped before its final absolute counters seed
// the replacement's nft rules.
func (m *Manager) Ensure(ctx context.Context, instance Instance) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ensureLocked(ctx, instance)
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
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stopLocked(ctx, id, intentional)
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
}

// Reconcile stops no-longer-desired sidecars then converges the desired set.
func (m *Manager) Reconcile(ctx context.Context, desired []Instance) error {
	want := make(map[int]bool, len(desired))
	for _, instance := range desired {
		want[instance.ID] = true
	}
	m.mu.Lock()
	for id := range m.byID {
		if want[id] {
			continue
		}
		if err := m.stopLocked(ctx, id, true); err != nil {
			m.mu.Unlock()
			return err
		}
		if m.Nft != nil {
			if err := m.Nft.RemoveInbound(ctx, id); err != nil {
				m.mu.Unlock()
				return err
			}
		}
		delete(m.byID, id)
		_ = os.Remove(m.configPath(id))
	}
	m.mu.Unlock()
	for _, instance := range desired {
		if err := m.Ensure(ctx, instance); err != nil {
			return err
		}
	}
	return nil
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
	cur.status.LastError = err.Error()
	delay := cur.backoff.Next()
	cur.status.RestartAt = m.now().Add(delay)
	cur.generation++
	generation := cur.generation
	instance := cur.instance
	m.schedule(delay, func() { m.restartAfterBackoff(id, generation, instance) })
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

// ResetTraffic resets the private nft counters for one managed inbound.
func (m *Manager) ResetTraffic(ctx context.Context, id int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Nft == nil {
		return errors.New("Snell nft manager is unavailable")
	}
	return m.Nft.ResetInbound(ctx, id)
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
