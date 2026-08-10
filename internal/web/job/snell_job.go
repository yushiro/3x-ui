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
		counters, err := j.Runtime.ReadSnellCounters(ctx, row.ID)
		if err != nil {
			logger.Warning("snell job: read counters for inbound", row.ID, "failed:", err)
			continue
		}
		if err := j.Runtime.SyncSnellCounters(ctx, row.ID, counters); err != nil {
			logger.Warning("snell job: sync counters for inbound", row.ID, "failed:", err)
			continue
		}
		if err := j.Runtime.EnforceSnellQuota(ctx, row.ID); err != nil {
			logger.Warning("snell job: enforce quota for inbound", row.ID, "failed:", err)
		}
	}
}
