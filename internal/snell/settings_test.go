package snell

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestConfigInstanceFromInboundUsesOnlySnellSettings(t *testing.T) {
	inbound := &model.Inbound{
		Id:       7,
		Listen:   "0.0.0.0",
		Port:     443,
		Protocol: model.Snell,
		Settings: `{"psk":"0123456789abcdef"}`,
		Enable:   true,
		Up:       100,
		Down:     200,
		Total:    300,
	}
	got, err := InstanceFromInbound(inbound)
	if err != nil {
		t.Fatalf("InstanceFromInbound: %v", err)
	}
	if got.ID != 7 || got.Listen != "0.0.0.0" || got.Port != 443 || got.PSK != "0123456789abcdef" || !got.Enable || got.Up != 100 || got.Down != 200 || got.Total != 300 {
		t.Fatalf("unexpected instance: %+v", got)
	}

	inbound.Protocol = model.VLESS
	if _, err := InstanceFromInbound(inbound); err == nil {
		t.Fatal("non-Snell inbound was accepted")
	}
}
