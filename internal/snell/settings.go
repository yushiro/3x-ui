// Package snell owns the local runtime boundary for Snell inbounds.
package snell

import (
	"fmt"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// Instance is the runtime representation of one Snell inbound.
type Instance struct {
	ID     int
	Listen string
	Port   int
	PSK    string
	Enable bool
	Up     int64
	Down   int64
	Total  int64
}

// InstanceFromInbound adapts the persisted Snell inbound into its runtime
// representation. The settings contract remains owned by model.
func InstanceFromInbound(inbound *model.Inbound) (Instance, error) {
	if inbound == nil || inbound.Protocol != model.Snell {
		return Instance{}, fmt.Errorf("not a Snell inbound")
	}
	settings, err := model.ParseSnellSettings(inbound.Settings)
	if err != nil {
		return Instance{}, fmt.Errorf("invalid Snell inbound settings")
	}
	return Instance{
		ID:     inbound.Id,
		Listen: inbound.Listen,
		Port:   inbound.Port,
		PSK:    settings.PSK,
		Enable: inbound.Enable,
		Up:     inbound.Up,
		Down:   inbound.Down,
		Total:  inbound.Total,
	}, nil
}
