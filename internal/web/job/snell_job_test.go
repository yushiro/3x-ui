package job

import (
	"context"
	"errors"
	goruntime "runtime"
	"sync"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/snell"
)

type fakeSnellClock struct{ now time.Time }

func (c fakeSnellClock) Now() time.Time { return c.now }

type fakeSnellRuntime struct {
	desired    []snell.Instance
	counters   map[int]snell.Counters
	reconcile  int
	synced     []int
	enforced   []int
	syncErr    error
	readErr    map[int]error
	resetCalls []struct {
		id      int
		monthly bool
	}
}

func (f *fakeSnellRuntime) DesiredSnellInstances() ([]snell.Instance, error) { return f.desired, nil }
func (f *fakeSnellRuntime) ReconcileSnell(context.Context, []snell.Instance) error {
	f.reconcile++
	return nil
}

func (*fakeSnellRuntime) BeginSnellLifecycle(ctx context.Context, _ int) (context.Context, func(), error) {
	return ctx, func() {}, nil
}

func (f *fakeSnellRuntime) ReadSnellCounters(_ context.Context, id int) (snell.Counters, error) {
	if err := f.readErr[id]; err != nil {
		return snell.Counters{}, err
	}
	return f.counters[id], nil
}

func (f *fakeSnellRuntime) SyncSnellCounters(_ context.Context, id int, _ snell.Counters) error {
	f.synced = append(f.synced, id)
	return f.syncErr
}

func (f *fakeSnellRuntime) EnforceSnellQuota(_ context.Context, id int) error {
	f.enforced = append(f.enforced, id)
	return nil
}

func (f *fakeSnellRuntime) ResetSnellTraffic(_ context.Context, id int, monthly bool) error {
	f.resetCalls = append(f.resetCalls, struct {
		id      int
		monthly bool
	}{id, monthly})
	return nil
}

func TestSnellJobReconcilesThenSyncsAndEnforcesEachReadableInbound(t *testing.T) {
	fake := &fakeSnellRuntime{
		desired:  []snell.Instance{{ID: 1}, {ID: 2}},
		counters: map[int]snell.Counters{1: {UpBytes: 7, DownBytes: 9}, 2: {UpBytes: 11, DownBytes: 13}},
		readErr:  map[int]error{},
	}
	NewSnellJob(fake, fakeSnellClock{}).Run()
	if fake.reconcile != 1 || len(fake.synced) != 2 || len(fake.enforced) != 2 {
		t.Fatalf("job calls: reconcile=%d synced=%v enforced=%v", fake.reconcile, fake.synced, fake.enforced)
	}
}

func TestSnellJobRetriesUnchangedCounterAfterSyncFailure(t *testing.T) {
	fake := &fakeSnellRuntime{
		desired:  []snell.Instance{{ID: 3}},
		counters: map[int]snell.Counters{3: {UpBytes: 17, DownBytes: 19}},
		readErr:  map[int]error{},
		syncErr:  errors.New("database unavailable"),
	}
	job := NewSnellJob(fake, fakeSnellClock{})
	job.Run()
	job.Run()
	if len(fake.synced) != 2 || len(fake.enforced) != 0 {
		t.Fatalf("failed writes must retry without quota action: synced=%v enforced=%v", fake.synced, fake.enforced)
	}
}

type collectionLifecycleKey struct{}

type collectionLifecycleRuntime struct {
	lifecycle sync.Mutex
	mu        sync.Mutex

	readDone     chan struct{}
	releaseRead  chan struct{}
	resetReached chan struct{}
	readOnce     sync.Once
	resetOnce    sync.Once

	counter snell.Counters
	stored  snell.Counters
	used    map[string]bool
}

func (f *collectionLifecycleRuntime) DesiredSnellInstances() ([]snell.Instance, error) {
	return []snell.Instance{{ID: 1}}, nil
}

func (*collectionLifecycleRuntime) ReconcileSnell(context.Context, []snell.Instance) error {
	return nil
}

func (f *collectionLifecycleRuntime) BeginSnellLifecycle(ctx context.Context, _ int) (context.Context, func(), error) {
	f.lifecycle.Lock()
	return context.WithValue(ctx, collectionLifecycleKey{}, true), f.lifecycle.Unlock, nil
}

func (f *collectionLifecycleRuntime) ReadSnellCounters(ctx context.Context, _ int) (snell.Counters, error) {
	f.mu.Lock()
	f.used["read"] = ctx.Value(collectionLifecycleKey{}) == true
	f.mu.Unlock()
	f.readOnce.Do(func() { close(f.readDone) })
	<-f.releaseRead
	return f.counter, nil
}

func (f *collectionLifecycleRuntime) SyncSnellCounters(ctx context.Context, _ int, counters snell.Counters) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.used["sync"] = ctx.Value(collectionLifecycleKey{}) == true
	f.stored = counters
	return nil
}

func (f *collectionLifecycleRuntime) EnforceSnellQuota(ctx context.Context, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.used["quota"] = ctx.Value(collectionLifecycleKey{}) == true
	return nil
}

func (f *collectionLifecycleRuntime) ResetSnellTraffic(ctx context.Context, id int, _ bool) error {
	ctx, release, err := f.BeginSnellLifecycle(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	_ = ctx
	f.resetOnce.Do(func() { close(f.resetReached) })
	f.mu.Lock()
	f.stored = snell.Counters{}
	f.mu.Unlock()
	return nil
}

func TestSnellJobCollectionHoldsLifecycleAcrossReadSyncQuota(t *testing.T) {
	previousMaxProcs := goruntime.GOMAXPROCS(1)
	t.Cleanup(func() { goruntime.GOMAXPROCS(previousMaxProcs) })
	fake := &collectionLifecycleRuntime{
		readDone:     make(chan struct{}),
		releaseRead:  make(chan struct{}),
		resetReached: make(chan struct{}),
		counter:      snell.Counters{UpBytes: 100, DownBytes: 200},
		used:         make(map[string]bool),
	}
	jobDone := make(chan struct{})
	go func() {
		NewSnellJob(fake, nil).Run()
		close(jobDone)
	}()
	<-fake.readDone

	resetDone := make(chan error, 1)
	go func() { resetDone <- fake.ResetSnellTraffic(context.Background(), 1, false) }()
	// The old job released the read before its sync. It therefore allowed reset
	// to reach its zeroing operation while stale absolute values were pending.
	goruntime.Gosched()
	select {
	case <-fake.resetReached:
		close(fake.releaseRead)
		<-jobDone
		<-resetDone
		t.Fatal("reset interleaved before the collection synced its counters")
	default:
	}
	close(fake.releaseRead)
	<-jobDone
	if err := <-resetDone; err != nil {
		t.Fatalf("reset: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.stored != (snell.Counters{}) {
		t.Fatalf("collection restored stale counters after reset: %+v", fake.stored)
	}
	for _, phase := range []string{"read", "sync", "quota"} {
		if !fake.used[phase] {
			t.Fatalf("collection phase %s did not receive lifecycle context", phase)
		}
	}
}
