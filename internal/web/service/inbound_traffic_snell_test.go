package service

import (
	"context"
	"sync"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/snell"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type resetSnellProcess struct{}

func (resetSnellProcess) Stop(context.Context) error { return nil }
func (resetSnellProcess) Wait() error                { select {} }
func (resetSnellProcess) Running() bool              { return true }

type resetSnellLauncher struct{ starts int }

func (l *resetSnellLauncher) Start(context.Context, string, string) (snell.ManagedProcess, error) {
	l.starts++
	return resetSnellProcess{}, nil
}

type resetSnellHost struct{}

func (resetSnellHost) Check(context.Context) error { return nil }

type resetSnellNft struct {
	mu    sync.Mutex
	calls int
}

func (n *resetSnellNft) Run(context.Context, ...string) ([]byte, error) {
	n.mu.Lock()
	n.calls++
	n.mu.Unlock()
	return []byte(`{"nftables":[]}`), nil
}

func setupSnellResetRuntime(t *testing.T) *resetSnellLauncher {
	t.Helper()
	previous := runtime.GetManager()
	launcher := &resetSnellLauncher{}
	manager := snell.NewManager(launcher, resetSnellHost{}, &snell.NftManager{Exec: &resetSnellNft{}}, "/bin/snell-server", t.TempDir())
	runtime.SetManager(runtime.NewManager(runtime.LocalDeps{Snell: manager}))
	t.Cleanup(func() { runtime.SetManager(previous) })
	return launcher
}

func TestSnellTrafficUsesAbsoluteCountersWithoutRollbackOrClients(t *testing.T) {
	setupConflictDB(t)
	previousRuntime := runtime.GetManager()
	mgr := runtime.NewManager(runtime.LocalDeps{})
	mgr.SetLocalRuntimeOverride(&fakeNodeRuntime{})
	runtime.SetManager(mgr)
	t.Cleanup(func() { runtime.SetManager(previousRuntime) })
	ib := &model.Inbound{Tag: "snell-traffic", Enable: true, Port: 443, Protocol: model.Snell, Settings: `{"psk":"valid-psk-12345678"}`, Total: 300}
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatal(err)
	}
	svc := &InboundService{}
	if err := svc.SyncSnellCounters(context.Background(), ib.Id, snell.Counters{UpBytes: 200, DownBytes: 150}); err != nil {
		t.Fatal(err)
	}
	if err := svc.EnforceSnellQuota(context.Background(), ib.Id); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncSnellCounters(context.Background(), ib.Id, snell.Counters{UpBytes: 10, DownBytes: 20}); err != nil {
		t.Fatal(err)
	}
	var got model.Inbound
	if err := database.GetDB().First(&got, ib.Id).Error; err != nil {
		t.Fatal(err)
	}
	if got.Up != 200 || got.Down != 150 || got.Enable {
		t.Fatalf("absolute quota state = %+v", got)
	}
	var clients int64
	if err := database.GetDB().Model(&xray.ClientTraffic{}).Where("inbound_id = ?", ib.Id).Count(&clients).Error; err != nil || clients != 0 {
		t.Fatalf("Snell created client traffic rows: %d, %v", clients, err)
	}
}

func TestSnellTrafficQuotaUsesPersistedAbsoluteMaximum(t *testing.T) {
	setupConflictDB(t)
	previousRuntime := runtime.GetManager()
	mgr := runtime.NewManager(runtime.LocalDeps{})
	fake := &fakeNodeRuntime{}
	mgr.SetLocalRuntimeOverride(fake)
	runtime.SetManager(mgr)
	t.Cleanup(func() { runtime.SetManager(previousRuntime) })
	ib := &model.Inbound{Tag: "snell-max", Enable: true, Port: 444, Protocol: model.Snell, Settings: `{"psk":"valid-psk-12345678"}`, Total: 100, Up: 80, Down: 30}
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatal(err)
	}
	if err := (&InboundService{}).SyncSnellCounters(context.Background(), ib.Id, snell.Counters{UpBytes: 50, DownBytes: 0}); err != nil {
		t.Fatal(err)
	}
	if err := (&InboundService{}).EnforceSnellQuota(context.Background(), ib.Id); err != nil {
		t.Fatal(err)
	}
	var got model.Inbound
	if err := database.GetDB().First(&got, ib.Id).Error; err != nil {
		t.Fatal(err)
	}
	if got.Enable {
		t.Fatalf("quota must disable using persisted maximum, got %+v", got)
	}
	if fake.delInbound.Load() != 1 {
		t.Fatalf("quota sidecar stop calls = %d, want 1", fake.delInbound.Load())
	}
}

func TestManualSnellResetClearsCountersWithoutChangingEnable(t *testing.T) {
	setupConflictDB(t)
	setupSnellResetRuntime(t)
	ib := &model.Inbound{Tag: "snell-manual-reset", Enable: false, Port: 445, Protocol: model.Snell, Settings: `{"psk":"valid-psk-12345678"}`, Up: 44, Down: 55}
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatal(err)
	}
	if err := (&InboundService{}).ResetInboundTraffic(ib.Id); err != nil {
		t.Fatal(err)
	}
	var got model.Inbound
	if err := database.GetDB().First(&got, ib.Id).Error; err != nil {
		t.Fatal(err)
	}
	if got.Up != 0 || got.Down != 0 || got.Enable {
		t.Fatalf("manual reset changed unexpected state: %+v", got)
	}
}

func TestMonthlySnellResetReenablesAndStartsValidInbound(t *testing.T) {
	setupConflictDB(t)
	launcher := setupSnellResetRuntime(t)
	ib := &model.Inbound{Tag: "snell-monthly-reset", Enable: false, Port: 446, Protocol: model.Snell, Settings: `{"psk":"valid-psk-12345678"}`, Up: 66, Down: 77, TrafficReset: "monthly"}
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatal(err)
	}
	if err := (&InboundService{}).ResetInboundTrafficForPeriod(ib.Id, true); err != nil {
		t.Fatal(err)
	}
	var got model.Inbound
	if err := database.GetDB().First(&got, ib.Id).Error; err != nil {
		t.Fatal(err)
	}
	if got.Up != 0 || got.Down != 0 || !got.Enable || launcher.starts != 1 {
		t.Fatalf("monthly reset state: inbound=%+v starts=%d", got, launcher.starts)
	}
}
