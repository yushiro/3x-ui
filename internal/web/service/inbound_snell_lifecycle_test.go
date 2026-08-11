package service

import (
	"context"
	"errors"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm"

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

type hostAwareRuntime struct {
	fakeNodeRuntime
	hostErr error
}

func (r *hostAwareRuntime) CheckSnellHost(context.Context) error { return r.hostErr }

func TestUpdateToEnabledSnellRejectsHostFailureBeforeReplacingOldRuntime(t *testing.T) {
	setupConflictDB(t)
	previousRuntime := runtime.GetManager()
	fake := &hostAwareRuntime{hostErr: errors.New("nftables permission check failed")}
	mgr := runtime.NewManager(runtime.LocalDeps{})
	mgr.SetLocalRuntimeOverride(fake)
	runtime.SetManager(mgr)
	t.Cleanup(func() { runtime.SetManager(previousRuntime) })

	old := &model.Inbound{
		Tag: "vless-to-snell-host-preflight", Enable: true, Port: 457,
		Protocol: model.VLESS, StreamSettings: `{"network":"tcp"}`, Settings: `{"clients":[]}`,
	}
	if err := database.GetDB().Create(old).Error; err != nil {
		t.Fatal(err)
	}
	update := *old
	update.Protocol = model.Snell
	update.Settings = `{"psk":"valid-psk-12345678"}`
	if _, _, err := (&InboundService{}).UpdateInbound(&update); err == nil || !strings.Contains(err.Error(), "nftables permission check failed") {
		t.Fatalf("enabled Snell conversion must return host prerequisite error, got %v", err)
	}
	var got model.Inbound
	if err := database.GetDB().First(&got, old.Id).Error; err != nil {
		t.Fatal(err)
	}
	if got.Protocol != model.VLESS || !got.Enable {
		t.Fatalf("host failure changed old inbound: %+v", got)
	}
	if fake.delInbound.Load() != 0 || fake.addInbound.Load() != 0 {
		t.Fatalf("host failure replaced old runtime: del=%d add=%d", fake.delInbound.Load(), fake.addInbound.Load())
	}
}

func TestUpdateSnellPortConflictLeavesOldSidecarRunning(t *testing.T) {
	setupConflictDB(t)
	ib := &model.Inbound{Tag: "snell-port-conflict", Enable: true, Port: 458, Protocol: model.Snell, Settings: `{"psk":"valid-psk-12345678"}`}
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatal(err)
	}
	launcher, nft := startLifecycleSnell(t, ib)
	nft.output = []byte(`{"nftables":[{"counter":{"name":"snell_1_up","bytes":10}},{"counter":{"name":"snell_1_down","bytes":20}}]}`)
	seedInboundConflict(t, "occupied-snell-port", "", 459, model.VLESS, `{"network":"tcp"}`, `{"clients":[]}`)

	update := *ib
	update.Port = 459
	if _, _, err := (&InboundService{}).UpdateInbound(&update); err == nil || !strings.Contains(err.Error(), "port") {
		t.Fatalf("conflicting Snell port update must fail, got %v", err)
	}
	if launcher.procs[0].stops != 0 {
		t.Fatalf("port conflict stopped old sidecar %d time(s)", launcher.procs[0].stops)
	}
	var got model.Inbound
	if err := database.GetDB().First(&got, ib.Id).Error; err != nil {
		t.Fatal(err)
	}
	if got.Port != 458 || !got.Enable {
		t.Fatalf("port conflict changed persisted inbound: %+v", got)
	}
}

type resetInterleaveNft struct {
	mu              sync.Mutex
	resetCalls      int
	zero            bool
	resetDone       chan struct{}
	readDuringReset chan struct{}
	observeRead     bool
	once            sync.Once
	readOnce        sync.Once
}

func (n *resetInterleaveNft) Run(_ context.Context, args ...string) ([]byte, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(args) > 0 && args[0] == "reset" {
		n.resetCalls++
		if n.resetCalls == 2 {
			n.zero = true
			n.once.Do(func() { close(n.resetDone) })
		}
	}
	if n.observeRead && len(args) >= 3 && args[0] == "-j" && args[1] == "list" && args[2] == "counters" {
		n.readOnce.Do(func() { close(n.readDuringReset) })
	}
	if n.zero {
		return []byte(`{"nftables":[{"counter":{"name":"snell_1_up","bytes":0}},{"counter":{"name":"snell_1_down","bytes":0}}]}`), nil
	}
	return []byte(`{"nftables":[{"counter":{"name":"snell_1_up","bytes":100}},{"counter":{"name":"snell_1_down","bytes":200}}]}`), nil
}

func TestResetSnellTrafficSerializesConcurrentUpdateUntilZeroSeeded(t *testing.T) {
	previousMaxProcs := goruntime.GOMAXPROCS(1)
	t.Cleanup(func() { goruntime.GOMAXPROCS(previousMaxProcs) })
	setupConflictDB(t)
	previousRuntime := runtime.GetManager()
	launcher := &resetSnellLauncher{}
	nft := &resetInterleaveNft{resetDone: make(chan struct{}), readDuringReset: make(chan struct{})}
	manager := snell.NewManager(launcher, resetSnellHost{}, &snell.NftManager{Exec: nft}, "/bin/snell-server", t.TempDir())
	runtime.SetManager(runtime.NewManager(runtime.LocalDeps{Snell: manager}))
	t.Cleanup(func() { runtime.SetManager(previousRuntime) })

	ib := &model.Inbound{Tag: "snell-reset-interleave", Enable: true, Port: 460, Protocol: model.Snell, Settings: `{"psk":"valid-psk-12345678"}`, Up: 100, Down: 200}
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.GetManager().RuntimeFor(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.AddInbound(context.Background(), ib); err != nil {
		t.Fatalf("start Snell inbound: %v", err)
	}

	db := database.GetDB()
	const blockUpdate = "snell-reset-interleave-block-update"
	resetWriteStarted := make(chan struct{})
	releaseResetWrite := make(chan struct{})
	var blockOnce sync.Once
	if err := db.Callback().Update().Before("gorm:update").Register(blockUpdate, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "inbounds" {
			select {
			case <-nft.resetDone:
				blockOnce.Do(func() {
					close(resetWriteStarted)
					<-releaseResetWrite
				})
			default:
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(blockUpdate) })

	const observeQuery = "snell-reset-interleave-observe-query"
	updateLoaded := make(chan struct{})
	var queryOnce sync.Once
	if err := db.Callback().Query().Before("gorm:query").Register(observeQuery, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "inbounds" {
			select {
			case <-resetWriteStarted:
				queryOnce.Do(func() { close(updateLoaded) })
			default:
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(observeQuery) })

	resetResult := make(chan error, 1)
	go func() { resetResult <- (&InboundService{}).ResetSnellTraffic(context.Background(), ib.Id, false) }()
	<-resetWriteStarted
	nft.mu.Lock()
	nft.observeRead = true
	nft.mu.Unlock()

	update := *ib
	update.Port = 461
	updateResult := make(chan error, 1)
	go func() {
		_, _, err := (&InboundService{}).UpdateInbound(&update)
		updateResult <- err
	}()
	<-updateLoaded
	// With one logical processor the update keeps executing until it blocks.
	// The old code reaches ReadSnellCounters before reset commits; the lifecycle
	// boundary must instead leave it waiting on the reset transaction.
	goruntime.Gosched()
	select {
	case <-nft.readDuringReset:
		close(releaseResetWrite)
		<-resetResult
		<-updateResult
		t.Fatal("concurrent update read counters before reset committed")
	default:
	}
	close(releaseResetWrite)
	if err := <-resetResult; err != nil {
		t.Fatalf("reset traffic: %v", err)
	}
	if err := <-updateResult; err != nil {
		t.Fatalf("concurrent update: %v", err)
	}

	var got model.Inbound
	if err := database.GetDB().First(&got, ib.Id).Error; err != nil {
		t.Fatal(err)
	}
	if got.Up != 0 || got.Down != 0 {
		t.Fatalf("concurrent update restored pre-reset traffic: %+v", got)
	}
}
