package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func seedVlessInbound(t *testing.T, tag string, port int, clients []model.Client) {
	t.Helper()
	setupSettingTestDB(t)
	db := database.GetDB()
	in := &model.Inbound{
		Tag:      tag,
		Enable:   true,
		Port:     port,
		Protocol: model.VLESS,
		Settings: `{"clients":[],"decryption":"none"}`,
	}
	if err := db.Create(in).Error; err != nil {
		t.Fatalf("create vless inbound: %v", err)
	}
	svc := ClientService{}
	if err := svc.SyncInbound(nil, in.Id, clients); err != nil {
		t.Fatalf("SyncInbound: %v", err)
	}
}

func disableClients(t *testing.T, emails ...string) {
	t.Helper()
	if err := database.GetDB().Model(&model.ClientRecord{}).
		Where("email IN ?", emails).
		Update("enable", false).Error; err != nil {
		t.Fatalf("disable clients: %v", err)
	}
}

func emittedClients(t *testing.T, tag string) (any, bool) {
	t.Helper()
	svc := &XrayService{}
	cfg, err := svc.GetXrayConfig()
	if err != nil {
		t.Fatalf("GetXrayConfig: %v", err)
	}
	for i := range cfg.InboundConfigs {
		if cfg.InboundConfigs[i].Tag != tag {
			continue
		}
		var s map[string]any
		if err := json.Unmarshal([]byte(cfg.InboundConfigs[i].Settings), &s); err != nil {
			t.Fatalf("unmarshal emitted settings: %v", err)
		}
		v, ok := s["clients"]
		return v, ok
	}
	t.Fatalf("inbound %q not found in generated config", tag)
	return nil, false
}

func TestGetXrayConfig_EmptyClientListIsArrayNotNull(t *testing.T) {
	seedVlessInbound(t, "vless-empty", 43101, []model.Client{
		{Email: "gone@x", ID: "11111111-1111-1111-1111-111111111111", Enable: true},
	})
	disableClients(t, "gone@x")

	value, present := emittedClients(t, "vless-empty")
	if !present {
		t.Fatal("the clients key must be present on a vless inbound")
	}
	if value == nil {
		t.Fatal(`settings.clients must be [] when every client is filtered out, got null`)
	}
	list, ok := value.([]any)
	if !ok {
		t.Fatalf("settings.clients must be an array, got %T", value)
	}
	if len(list) != 0 {
		t.Fatalf("a disabled client must not reach the config, got %d entries", len(list))
	}
}

func TestGetXrayConfig_EnabledClientsStillEmitted(t *testing.T) {
	seedVlessInbound(t, "vless-mixed", 43102, []model.Client{
		{Email: "live@x", ID: "22222222-2222-2222-2222-222222222222", Enable: true},
		{Email: "off@x", ID: "33333333-3333-3333-3333-333333333333", Enable: true},
	})
	disableClients(t, "off@x")

	value, _ := emittedClients(t, "vless-mixed")
	list, ok := value.([]any)
	if !ok {
		t.Fatalf("settings.clients must be an array, got %T", value)
	}
	if len(list) != 1 {
		t.Fatalf("expected only the enabled client, got %d entries: %#v", len(list), list)
	}
	entry, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("client entry must be an object, got %T", list[0])
	}
	if entry["email"] != "live@x" {
		t.Errorf("wrong client emitted: %#v", entry)
	}
	if entry["id"] != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("client id not carried through: %#v", entry)
	}
}

func TestGetXrayConfig_StandAloneSnellNotIncluded(t *testing.T) {
	setupSettingTestDB(t)
	db := database.GetDB()
	snellTag := "snell-local"
	psk := "secret-psk-12345678"

	vlessInbound := &model.Inbound{
		Tag:      "vless-normal",
		Enable:   true,
		Port:     52001,
		Protocol: model.VLESS,
		Settings: `{"clients":[],"decryption":"none"}`,
	}
	if err := db.Create(vlessInbound).Error; err != nil {
		t.Fatalf("create vless inbound: %v", err)
	}

	snellInbound := &model.Inbound{
		Tag:      snellTag,
		Enable:   true,
		Port:     52002,
		Protocol: model.Snell,
		Settings: `{"psk":"` + psk + `"}`,
	}
	if err := db.Create(snellInbound).Error; err != nil {
		t.Fatalf("create snell inbound: %v", err)
	}

	svc := &XrayService{}
	cfg, err := svc.GetXrayConfig()
	if err != nil {
		t.Fatalf("GetXrayConfig: %v", err)
	}

	foundVless := false
	for _, inbound := range cfg.InboundConfigs {
		if inbound.Tag == snellTag {
			t.Fatalf("standalone snell inbound was unexpectedly injected: %q", inbound.Tag)
		}
		if inbound.Protocol == string(model.Snell) {
			t.Fatalf("standalone snell protocol was unexpectedly emitted: %q", inbound.Protocol)
		}
		if strings.Contains(string(inbound.Settings), psk) {
			t.Fatalf("snell PSK leaked into emitted inbound settings: %q", inbound.Settings)
		}

		if inbound.Tag != "vless-normal" {
			continue
		}
		var settings map[string]any
		if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
			t.Fatalf("unmarshal emitted vless settings: %v", err)
		}
		if inbound.Protocol != string(model.VLESS) {
			t.Fatalf("expected emitted protocol %q for normal inbound, got %q", model.VLESS, inbound.Protocol)
		}
		if inbound.Tag != "vless-normal" {
			t.Fatalf("expected normal vless tag preserved, got %q", inbound.Tag)
		}
		if settings == nil {
			t.Fatal("normal inbound settings must be emitted")
		}
		foundVless = true
	}

	if !foundVless {
		t.Fatal("normal local inbound was not emitted while building xray config")
	}
}
