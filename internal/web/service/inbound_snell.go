package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/snell"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"gorm.io/gorm"
)

func (s *InboundService) snellRuntimeFor(inbound *model.Inbound) (runtime.Runtime, error) {
	if inbound == nil || inbound.Protocol != model.Snell || inbound.NodeID != nil {
		return nil, errors.New("Snell sidecars are available only for local inbounds")
	}
	if runtime.GetManager() == nil {
		return nil, errors.New("Snell runtime is unavailable")
	}
	return runtime.GetManager().RuntimeFor(nil)
}

func (s *InboundService) ValidateSnell(inbound *model.Inbound, _ bool) error {
	if inbound == nil || inbound.Protocol != model.Snell {
		return nil
	}
	if inbound.NodeID != nil {
		return errors.New("Snell is supported only on this panel")
	}
	_, err := model.ParseSnellSettings(inbound.Settings)
	return err
}

func (s *InboundService) DesiredSnellInstances() ([]snell.Instance, error) {
	var inbounds []*model.Inbound
	if err := database.GetDB().Where("protocol = ? AND node_id IS NULL", model.Snell).Find(&inbounds).Error; err != nil {
		return nil, err
	}
	out := make([]snell.Instance, 0, len(inbounds))
	for _, inbound := range inbounds {
		instance, err := snell.InstanceFromInbound(inbound)
		if err != nil {
			return nil, err
		}
		out = append(out, instance)
	}
	return out, nil
}

func (s *InboundService) ApplySnellInbound(ctx context.Context, oldInbound, inbound *model.Inbound) error {
	rt, err := s.snellRuntimeFor(inbound)
	if err != nil {
		return err
	}
	if oldInbound == nil {
		return rt.AddInbound(ctx, inbound)
	}
	return rt.UpdateInbound(ctx, oldInbound, inbound)
}

func (s *InboundService) checkSnellHost(ctx context.Context, inbound *model.Inbound) error {
	rt, err := s.snellRuntimeFor(inbound)
	if err != nil {
		return err
	}
	checker, ok := rt.(interface{ CheckSnellHost(context.Context) error })
	if !ok {
		return errors.New("Snell runtime is unavailable")
	}
	return checker.CheckSnellHost(ctx)
}

func (s *InboundService) beginSnellLifecycle(ctx context.Context, inbound *model.Inbound) (context.Context, func(), error) {
	rt, err := s.snellRuntimeFor(inbound)
	if err != nil {
		return ctx, nil, err
	}
	lifecycle, ok := rt.(interface {
		BeginSnellLifecycle(context.Context, int) (context.Context, func(), error)
	})
	if !ok {
		return ctx, nil, errors.New("Snell runtime is unavailable")
	}
	return lifecycle.BeginSnellLifecycle(ctx, inbound.Id)
}

// stopAndReadSnellTraffic closes the accounting window before a lifecycle
// change, then returns the final absolute counters for the same inbound.
func (s *InboundService) stopAndReadSnellTraffic(ctx context.Context, inbound *model.Inbound) (snell.Counters, error) {
	rt, err := s.snellRuntimeFor(inbound)
	if err != nil {
		return snell.Counters{}, err
	}
	stopper, ok := rt.(interface {
		StopSnell(context.Context, int) error
	})
	if !ok {
		return snell.Counters{}, errors.New("Snell runtime is unavailable")
	}
	if err := stopper.StopSnell(ctx, inbound.Id); err != nil {
		return snell.Counters{}, err
	}
	reader, ok := rt.(interface {
		ReadSnellCounters(context.Context, int) (snell.Counters, error)
	})
	if !ok {
		return snell.Counters{}, errors.New("Snell runtime is unavailable")
	}
	return reader.ReadSnellCounters(ctx, inbound.Id)
}

func (s *InboundService) setSnellEnable(inbound *model.Inbound, enable bool) (bool, error) {
	ctx := context.Background()
	if enable {
		if err := s.checkSnellHost(ctx, inbound); err != nil {
			return false, err
		}
	}
	lifecycleCtx, release, err := s.beginSnellLifecycle(ctx, inbound)
	if err != nil {
		return false, err
	}
	defer release()
	ctx = lifecycleCtx
	if enable {
		if err := database.GetDB().Model(&model.Inbound{}).Where("id = ?", inbound.Id).Update("enable", true).Error; err != nil {
			return false, err
		}
		inbound.Enable = true
		rt, err := s.snellRuntimeFor(inbound)
		if err != nil {
			_ = database.GetDB().Model(&model.Inbound{}).Where("id = ?", inbound.Id).Update("enable", false).Error
			inbound.Enable = false
			return false, err
		}
		if err := rt.AddInbound(ctx, inbound); err != nil {
			_ = database.GetDB().Model(&model.Inbound{}).Where("id = ?", inbound.Id).Update("enable", false).Error
			inbound.Enable = false
			return false, err
		}
		return false, nil
	}

	counters, err := s.stopAndReadSnellTraffic(ctx, inbound)
	if err != nil {
		return false, err
	}
	if err := s.syncSnellCounters(ctx, inbound, counters); err != nil {
		return false, err
	}
	if err := database.GetDB().Model(&model.Inbound{}).Where("id = ?", inbound.Id).Update("enable", false).Error; err != nil {
		return false, err
	}
	inbound.Enable = false
	inbound.Up = max(inbound.Up, counters.UpBytes)
	inbound.Down = max(inbound.Down, counters.DownBytes)
	return false, nil
}

func redactSnellList(inbound *model.Inbound) {
	if inbound == nil || inbound.Protocol != model.Snell {
		return
	}
	var settings map[string]any
	if json.Unmarshal([]byte(inbound.Settings), &settings) != nil {
		return
	}
	delete(settings, "psk")
	if raw, err := json.Marshal(settings); err == nil {
		inbound.Settings = string(raw)
	}
}

func (s *InboundService) annotateSnellStatus(inbounds []*model.Inbound) {
	if runtime.GetManager() == nil {
		return
	}
	local, ok := runtime.GetManager().Local().(interface{ SnellStatus(int) snell.Status })
	if !ok {
		return
	}
	for _, inbound := range inbounds {
		if inbound.Protocol != model.Snell || inbound.NodeID != nil {
			continue
		}
		status := local.SnellStatus(inbound.Id)
		view := &model.InboundViewStatus{Running: status.Running}
		if status.LastError != "" {
			view.ErrorCategory = snellErrorCategory(status.LastError)
		}
		inbound.RuntimeStatus = view
	}
}

func snellErrorCategory(message string) string {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "docker"), strings.Contains(lower, "linux"), strings.Contains(lower, "architecture"):
		return "host"
	case strings.Contains(lower, "binary"):
		return "binary"
	case strings.Contains(lower, "nft"):
		return "nftables"
	default:
		return "runtime"
	}
}

func (s *InboundService) ReconcileSnell(ctx context.Context, desired []snell.Instance) error {
	if runtime.GetManager() == nil {
		return errors.New("Snell runtime is unavailable")
	}
	local, ok := runtime.GetManager().Local().(interface {
		ReconcileSnell(context.Context, []snell.Instance) error
	})
	if !ok {
		return errors.New("Snell runtime is unavailable")
	}
	return local.ReconcileSnell(ctx, desired)
}

func (s *InboundService) ReadSnellCounters(ctx context.Context, id int) (snell.Counters, error) {
	if runtime.GetManager() == nil {
		return snell.Counters{}, errors.New("Snell runtime is unavailable")
	}
	local, ok := runtime.GetManager().Local().(interface {
		ReadSnellCounters(context.Context, int) (snell.Counters, error)
	})
	if !ok {
		return snell.Counters{}, errors.New("Snell runtime is unavailable")
	}
	return local.ReadSnellCounters(ctx, id)
}

// SyncSnellCounters persists absolute nft counters without allowing a delayed
// lower read to roll back traffic already recorded in the database.
func (s *InboundService) SyncSnellCounters(ctx context.Context, id int, counters snell.Counters) error {
	inbound, err := s.GetInbound(id)
	if err != nil {
		return err
	}
	return s.syncSnellCounters(ctx, inbound, counters)
}

func (s *InboundService) syncSnellCounters(_ context.Context, inbound *model.Inbound, counters snell.Counters) error {
	if inbound == nil || inbound.Protocol != model.Snell || inbound.NodeID != nil || counters.UpBytes < 0 || counters.DownBytes < 0 {
		return errors.New("invalid Snell counter sync")
	}
	return submitTrafficWrite(func() error {
		return database.GetDB().Transaction(func(tx *gorm.DB) error {
			updates := map[string]any{
				"up":   gorm.Expr("CASE WHEN up > ? THEN up ELSE ? END", counters.UpBytes, counters.UpBytes),
				"down": gorm.Expr("CASE WHEN down > ? THEN down ELSE ? END", counters.DownBytes, counters.DownBytes),
			}
			if err := tx.Model(&model.Inbound{}).Where("id = ?", inbound.Id).Updates(updates).Error; err != nil {
				return err
			}
			return nil
		})
	})
}

// EnforceSnellQuota disables and stops only the depleted local sidecar. An
// unlimited (total=0) inbound is deliberately untouched.
func (s *InboundService) EnforceSnellQuota(ctx context.Context, id int) error {
	var inbound model.Inbound
	if err := database.GetDB().First(&inbound, id).Error; err != nil {
		return err
	}
	if inbound.Protocol != model.Snell || inbound.NodeID != nil {
		return errors.New("invalid Snell quota enforcement")
	}
	var stop bool
	err := submitTrafficWrite(func() error {
		result := database.GetDB().Model(&model.Inbound{}).
			Where("id = ? AND enable = ? AND total > 0 AND up + down >= total", id, true).
			Update("enable", false)
		stop = result.RowsAffected > 0
		return result.Error
	})
	if err != nil || !stop {
		return err
	}
	rt, err := s.snellRuntimeFor(&inbound)
	if err != nil {
		return err
	}
	if local, ok := rt.(interface {
		StopSnell(context.Context, int) error
	}); ok {
		return local.StopSnell(ctx, inbound.Id)
	}
	return rt.DelInbound(ctx, &inbound)
}

func (s *InboundService) ResetSnellTraffic(ctx context.Context, id int, monthly bool) error {
	var inbound model.Inbound
	if err := database.GetDB().Where("id = ? AND protocol = ? AND node_id IS NULL", id, model.Snell).First(&inbound).Error; err != nil {
		return err
	}
	if runtime.GetManager() == nil {
		return errors.New("Snell runtime is unavailable")
	}
	local, ok := runtime.GetManager().Local().(interface {
		ResetSnellTraffic(context.Context, int) error
	})
	if !ok {
		return errors.New("Snell runtime is unavailable")
	}
	lifecycleCtx, release, err := s.beginSnellLifecycle(ctx, &inbound)
	if err != nil {
		return err
	}
	defer release()
	ctx = lifecycleCtx
	if err := local.ResetSnellTraffic(ctx, id); err != nil {
		if inbound.Enable {
			if rt, runtimeErr := s.snellRuntimeFor(&inbound); runtimeErr == nil {
				_ = rt.AddInbound(ctx, &inbound)
			}
		}
		return err
	}
	updates := map[string]any{"up": 0, "down": 0}
	if monthly {
		updates["enable"] = true
	}
	if err := submitTrafficWrite(func() error {
		return database.GetDB().Model(&model.Inbound{}).Where("id = ?", id).Updates(updates).Error
	}); err != nil {
		if inbound.Enable {
			if rt, runtimeErr := s.snellRuntimeFor(&inbound); runtimeErr == nil {
				_ = rt.AddInbound(ctx, &inbound)
			}
		}
		return err
	}
	inbound.Up = 0
	inbound.Down = 0
	if monthly {
		inbound.Enable = true
	}
	if !inbound.Enable {
		return nil
	}
	if _, err := snell.InstanceFromInbound(&inbound); err != nil {
		return err
	}
	rt, err := s.snellRuntimeFor(&inbound)
	if err != nil {
		return err
	}
	return rt.AddInbound(ctx, &inbound)
}
