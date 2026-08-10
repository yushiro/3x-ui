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

// SyncSnellCounters persists absolute nft counters without allowing a delayed
// lower read to roll back traffic already recorded in the database.
func (s *InboundService) SyncSnellCounters(ctx context.Context, inbound *model.Inbound, counters snell.Counters) error {
	if inbound == nil || inbound.Protocol != model.Snell || inbound.NodeID != nil || counters.UpBytes < 0 || counters.DownBytes < 0 {
		return errors.New("invalid Snell counter sync")
	}
	var stop bool
	err := submitTrafficWrite(func() error {
		return database.GetDB().Transaction(func(tx *gorm.DB) error {
			updates := map[string]any{
				"up":   gorm.Expr("CASE WHEN up > ? THEN up ELSE ? END", counters.UpBytes, counters.UpBytes),
				"down": gorm.Expr("CASE WHEN down > ? THEN down ELSE ? END", counters.DownBytes, counters.DownBytes),
			}
			if err := tx.Model(&model.Inbound{}).Where("id = ?", inbound.Id).Updates(updates).Error; err != nil {
				return err
			}
			var persisted model.Inbound
			if err := tx.Select("id", "up", "down", "total", "enable").First(&persisted, inbound.Id).Error; err != nil {
				return err
			}
			if persisted.Total > 0 && persisted.Up+persisted.Down >= persisted.Total && persisted.Enable {
				if err := tx.Model(&model.Inbound{}).Where("id = ?", inbound.Id).Update("enable", false).Error; err != nil {
					return err
				}
				stop = true
			}
			return nil
		})
	})
	if err != nil || !stop {
		return err
	}
	rt, err := s.snellRuntimeFor(inbound)
	if err != nil {
		return err
	}
	if local, ok := rt.(interface {
		StopSnell(context.Context, int) error
	}); ok {
		return local.StopSnell(ctx, inbound.Id)
	}
	return rt.DelInbound(ctx, inbound)
}

func (s *InboundService) ResetSnellTraffic(ctx context.Context, id int, resetCounters bool) error {
	err := submitTrafficWrite(func() error {
		return database.GetDB().Model(&model.Inbound{}).Where("id = ? AND protocol = ? AND node_id IS NULL", id, model.Snell).Updates(map[string]any{"up": 0, "down": 0}).Error
	})
	if err != nil || !resetCounters || runtime.GetManager() == nil {
		return err
	}
	local, ok := runtime.GetManager().Local().(interface {
		ResetSnellTraffic(context.Context, int) error
	})
	if !ok {
		return errors.New("Snell runtime is unavailable")
	}
	return local.ResetSnellTraffic(ctx, id)
}
