package job

import (
	"context"
	"errors"
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
