package job

import (
	"context"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/snell"
)

// SnellRuntimeService is the small service boundary needed by the periodic
// sidecar reconciler. It keeps the job independent from process/nft details.
type SnellRuntimeService interface {
	DesiredSnellInstances() ([]snell.Instance, error)
	ReconcileSnell(context.Context, []snell.Instance) error
	BeginSnellLifecycle(context.Context, int) (context.Context, func(), error)
	ReadSnellCounters(context.Context, int) (snell.Counters, error)
	SyncSnellCounters(context.Context, int, snell.Counters) error
	EnforceSnellQuota(context.Context, int) error
	ResetSnellTraffic(context.Context, int, bool) error
}

// Clock makes periodic work deterministic in tests and leaves room for future
// time-based logging without coupling the job to package time.
type Clock interface{ Now() time.Time }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// SnellJob converges managed sidecars and persists their absolute nft counters.
type SnellJob struct {
	Runtime SnellRuntimeService
	Clock   Clock
}

func NewSnellJob(runtime SnellRuntimeService, clock Clock) *SnellJob {
	if clock == nil {
		clock = systemClock{}
	}
	return &SnellJob{Runtime: runtime, Clock: clock}
}

func (j *SnellJob) Run() {
	if j == nil || j.Runtime == nil {
		return
	}
	ctx := context.Background()
	rows, err := j.Runtime.DesiredSnellInstances()
	if err != nil {
		logger.Warning("snell job: get desired instances failed:", err)
		return
	}
	if err := j.Runtime.ReconcileSnell(ctx, rows); err != nil {
		logger.Warning("snell job: reconcile failed:", err)
		return
	}
	for _, row := range rows {
		if err := j.collectInbound(ctx, row.ID); err != nil {
			logger.Warning("snell job: collect traffic for inbound", row.ID, "failed:", err)
		}
	}
}

func (j *SnellJob) collectInbound(ctx context.Context, id int) error {
	ctx, release, err := j.Runtime.BeginSnellLifecycle(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	counters, err := j.Runtime.ReadSnellCounters(ctx, id)
	if err != nil {
		return err
	}
	if err := j.Runtime.SyncSnellCounters(ctx, id, counters); err != nil {
		return err
	}
	return j.Runtime.EnforceSnellQuota(ctx, id)
}
