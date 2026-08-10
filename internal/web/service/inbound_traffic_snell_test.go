package service

import (
	"context"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/snell"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

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
	if err := svc.SyncSnellCounters(context.Background(), ib, snell.Counters{UpBytes: 200, DownBytes: 150}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncSnellCounters(context.Background(), ib, snell.Counters{UpBytes: 10, DownBytes: 20}); err != nil {
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
	if err := (&InboundService{}).SyncSnellCounters(context.Background(), ib, snell.Counters{UpBytes: 50, DownBytes: 0}); err != nil {
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
