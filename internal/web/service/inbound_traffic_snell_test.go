package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/snell"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"gorm.io/gorm"
)

type resetSnellProcess struct {
	mu      sync.Mutex
	running bool
	stops   int
}

func (p *resetSnellProcess) Stop(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stops++
	p.running = false
	return nil
}
func (*resetSnellProcess) Wait() error { select {} }
func (p *resetSnellProcess) Running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

type resetSnellLauncher struct {
	starts int
	procs  []*resetSnellProcess
}

func (l *resetSnellLauncher) Start(context.Context, string, string) (snell.ManagedProcess, error) {
	l.starts++
	p := &resetSnellProcess{running: true}
	l.procs = append(l.procs, p)
	return p, nil
}

type resetSnellHost struct{}

func (resetSnellHost) Check(context.Context) error { return nil }

type resetSnellNft struct {
	mu            sync.Mutex
	calls         [][]string
	resetErr      error
	output        []byte
	clearOnDelete bool
}

func (n *resetSnellNft) Run(_ context.Context, args ...string) ([]byte, error) {
	n.mu.Lock()
	n.calls = append(n.calls, args)
	n.mu.Unlock()
	if len(args) > 0 && args[0] == "reset" && n.resetErr != nil {
		return nil, n.resetErr
	}
	if n.clearOnDelete && len(args) > 1 && args[0] == "delete" && args[1] == "counter" {
		n.output = []byte(`{"nftables":[]}`)
	}
	if n.output != nil {
		return n.output, nil
	}
	return []byte(`{"nftables":[]}`), nil
}

func (n *resetSnellNft) joined() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	parts := make([]string, 0, len(n.calls))
	for _, call := range n.calls {
		parts = append(parts, strings.Join(call, " "))
	}
	return strings.Join(parts, " ")
}

func (n *resetSnellNft) clear() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = nil
}

func setupSnellResetRuntime(t *testing.T) (*resetSnellLauncher, *resetSnellNft) {
	t.Helper()
	previous := runtime.GetManager()
	launcher := &resetSnellLauncher{}
	nft := &resetSnellNft{}
	manager := snell.NewManager(launcher, resetSnellHost{}, &snell.NftManager{Exec: nft}, "/bin/snell-server", t.TempDir())
	runtime.SetManager(runtime.NewManager(runtime.LocalDeps{Snell: manager}))
	t.Cleanup(func() { runtime.SetManager(previous) })
	return launcher, nft
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
	_, _ = setupSnellResetRuntime(t)
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
	launcher, _ := setupSnellResetRuntime(t)
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

func TestMonthlySnellResetStartsWithZeroCounterSeed(t *testing.T) {
	setupConflictDB(t)
	_, nft := setupSnellResetRuntime(t)
	ib := &model.Inbound{Tag: "snell-monthly-zero-seed", Enable: false, Port: 447, Protocol: model.Snell, Settings: `{"psk":"valid-psk-12345678"}`, Up: 66, Down: 77, TrafficReset: "monthly"}
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatal(err)
	}
	if err := (&InboundService{}).ResetInboundTrafficForPeriod(ib.Id, true); err != nil {
		t.Fatal(err)
	}
	joined := nft.joined()
	if strings.Contains(joined, "bytes 66") || strings.Contains(joined, "bytes 77") {
		t.Fatalf("monthly restart reseeded old traffic: %s", joined)
	}
	if !strings.Contains(joined, "bytes 0") {
		t.Fatalf("monthly reset did not create zero-seeded counters: %s", joined)
	}
}

func TestSnellResetLeavesDatabaseTrafficUntouchedWhenNftResetFails(t *testing.T) {
	setupConflictDB(t)
	_, nft := setupSnellResetRuntime(t)
	nft.resetErr = errors.New("nft reset failed")
	ib := &model.Inbound{Tag: "snell-reset-nft-failure", Enable: false, Port: 448, Protocol: model.Snell, Settings: `{"psk":"valid-psk-12345678"}`, Up: 44, Down: 55}
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatal(err)
	}
	if err := (&InboundService{}).ResetInboundTraffic(ib.Id); err == nil {
		t.Fatal("ResetInboundTraffic accepted nft failure")
	}
	var got model.Inbound
	if err := database.GetDB().First(&got, ib.Id).Error; err != nil {
		t.Fatal(err)
	}
	if got.Up != 44 || got.Down != 55 {
		t.Fatalf("nft failure lost persisted traffic: %+v", got)
	}
}

func TestSnellResetTouchesCountersBeforeReportingDatabaseFailure(t *testing.T) {
	setupConflictDB(t)
	_, nft := setupSnellResetRuntime(t)
	ib := &model.Inbound{Tag: "snell-reset-db-failure", Enable: false, Port: 449, Protocol: model.Snell, Settings: `{"psk":"valid-psk-12345678"}`, Up: 44, Down: 55}
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	const callback = "snell-reset-db-failure"
	if err := db.Callback().Update().After("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "inbounds" {
			tx.AddError(errors.New("database unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callback) })
	if err := (&InboundService{}).ResetInboundTraffic(ib.Id); err == nil {
		t.Fatal("ResetInboundTraffic accepted database failure")
	}
	if !strings.Contains(nft.joined(), "reset counter inet xui_snell snell_") {
		t.Fatalf("database failure must occur after counter reset attempt: %s", nft.joined())
	}
}

func TestResetAllTrafficsResetsSnellCountersAsWellAsDatabase(t *testing.T) {
	setupConflictDB(t)
	_, nft := setupSnellResetRuntime(t)
	ib := &model.Inbound{UserId: 1, Tag: "snell-global-reset", Enable: false, Port: 450, Protocol: model.Snell, Settings: `{"psk":"valid-psk-12345678"}`, Up: 11, Down: 12}
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatal(err)
	}
	if err := (&InboundService{}).ResetAllTraffics(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(nft.joined(), "reset counter inet xui_snell snell_") {
		t.Fatalf("global reset left Snell counters unchanged: %s", nft.joined())
	}
}
