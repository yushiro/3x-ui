package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/snell"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

func startLifecycleSnell(t *testing.T, inbound *model.Inbound) (*resetSnellLauncher, *resetSnellNft) {
	t.Helper()
	launcher, nft := setupSnellResetRuntime(t)
	rt, err := runtime.GetManager().RuntimeFor(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.AddInbound(context.Background(), inbound); err != nil {
		t.Fatalf("start Snell inbound: %v", err)
	}
	return launcher, nft
}

func TestSetInboundEnableSnellStopsAndPreservesFinalTraffic(t *testing.T) {
	setupConflictDB(t)
	ib := &model.Inbound{Tag: "snell-disable-final", Enable: true, Port: 451, Protocol: model.Snell, Settings: `{"psk":"valid-psk-12345678"}`, Up: 10, Down: 20}
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatal(err)
	}
	launcher, nft := startLifecycleSnell(t, ib)
	nft.clear()
	nft.output = []byte(`{"nftables":[{"counter":{"name":"snell_1_up","bytes":101}},{"counter":{"name":"snell_1_down","bytes":202}}]}`)
	if _, err := (&InboundService{}).SetInboundEnable(ib.Id, false); err != nil {
		t.Fatal(err)
	}
	var got model.Inbound
	if err := database.GetDB().First(&got, ib.Id).Error; err != nil {
		t.Fatal(err)
	}
	if got.Enable || got.Up != 101 || got.Down != 202 {
		t.Fatalf("manual disable did not preserve final traffic: %+v", got)
	}
	if launcher.procs[0].stops != 1 {
		t.Fatalf("manual disable did not stop sidecar: %+v", launcher.procs[0])
	}
	if strings.Contains(nft.joined(), "delete counter inet xui_snell") {
		t.Fatalf("manual disable removed owned counters: %s", nft.joined())
	}
}

func TestUpdateSnellPersistsFinalTrafficBeforeReplacingSidecar(t *testing.T) {
	setupConflictDB(t)
	ib := &model.Inbound{Tag: "snell-update-final", Enable: true, Port: 452, Protocol: model.Snell, Settings: `{"psk":"valid-psk-12345678"}`, Up: 10, Down: 20}
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatal(err)
	}
	_, nft := startLifecycleSnell(t, ib)
	nft.output = []byte(`{"nftables":[{"counter":{"name":"snell_1_up","bytes":111}},{"counter":{"name":"snell_1_down","bytes":222}}]}`)
	nft.clearOnDelete = true
	update := *ib
	update.Port = 453
	update.Settings = `{"psk":"replacement-psk-12345678"}`
	if _, _, err := (&InboundService{}).UpdateInbound(&update); err != nil {
		t.Fatal(err)
	}
	var got model.Inbound
	if err := database.GetDB().First(&got, ib.Id).Error; err != nil {
		t.Fatal(err)
	}
	if got.Up != 111 || got.Down != 222 {
		t.Fatalf("update lost between-poll traffic: %+v", got)
	}
	if !strings.Contains(nft.joined(), "snell_input tcp dport 453") {
		t.Fatalf("port update did not replace managed counter rules: %s", nft.joined())
	}
}

func TestDeleteDisabledSnellRemovesOwnedRuntimeObjects(t *testing.T) {
	setupConflictDB(t)
	_, nft := setupSnellResetRuntime(t)
	ib := &model.Inbound{Tag: "snell-delete-disabled", Enable: false, Port: 454, Protocol: model.Snell, Settings: `{"psk":"valid-psk-12345678"}`}
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := (&InboundService{}).DelInbound(ib.Id); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(nft.joined(), "delete counter inet xui_snell snell_1_up") {
		t.Fatalf("disabled Snell delete left owned counters/config unmanaged: %s", nft.joined())
	}
}

type failingSnellHost struct{ err error }

func (h failingSnellHost) Check(context.Context) error { return h.err }

func setupFailingSnellRuntime(t *testing.T, err error) {
	t.Helper()
	previous := runtime.GetManager()
	manager := snell.NewManager(&resetSnellLauncher{}, failingSnellHost{err: err}, &snell.NftManager{Exec: &resetSnellNft{}}, "/bin/snell-server", t.TempDir())
	runtime.SetManager(runtime.NewManager(runtime.LocalDeps{Snell: manager, APIPort: func() int { return 0 }}))
	t.Cleanup(func() { runtime.SetManager(previous) })
}

func TestEnabledSnellCreateRejectsEveryHostPrerequisiteFailureBeforePersisting(t *testing.T) {
	for _, hostErr := range []string{
		"Snell is unsupported in Docker",
		"Snell is supported only on Linux hosts",
		"Snell binary unavailable",
		"nftables is unavailable",
		"nftables permission check failed",
	} {
		t.Run(hostErr, func(t *testing.T) {
			setupConflictDB(t)
			setupFailingSnellRuntime(t, errors.New(hostErr))
			ib := &model.Inbound{Tag: "snell-host-reject", Enable: true, Port: 455, Protocol: model.Snell, Settings: `{"psk":"valid-psk-12345678"}`}
			if _, _, err := (&InboundService{}).AddInbound(ib); err == nil || !strings.Contains(err.Error(), hostErr) {
				t.Fatalf("enabled create must return host prerequisite error %q, got %v", hostErr, err)
			}
			var count int64
			if err := database.GetDB().Model(&model.Inbound{}).Where("tag = ?", ib.Tag).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("failed enabled Snell create persisted %d inbound(s)", count)
			}
		})
	}
}

func TestEnabledSnellTransitionRejectsHostFailureWithoutChangingEnable(t *testing.T) {
	setupConflictDB(t)
	setupFailingSnellRuntime(t, errors.New("nftables permission check failed"))
	ib := &model.Inbound{Tag: "snell-enable-host-reject", Enable: false, Port: 456, Protocol: model.Snell, Settings: `{"psk":"valid-psk-12345678"}`}
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := (&InboundService{}).SetInboundEnable(ib.Id, true); err == nil {
		t.Fatal("enabled transition accepted host prerequisite failure")
	}
	var got model.Inbound
	if err := database.GetDB().First(&got, ib.Id).Error; err != nil {
		t.Fatal(err)
	}
	if got.Enable {
		t.Fatalf("failed enabled transition persisted enable=true: %+v", got)
	}
}
