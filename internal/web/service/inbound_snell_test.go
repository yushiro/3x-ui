package service

import (
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestSnellListRedactsButDetailKeepsPSK(t *testing.T) {
	list := &model.Inbound{Protocol: model.Snell, Settings: `{"psk":"secret-psk-123456"}`}
	redactSnellList(list)
	if strings.Contains(list.Settings, "secret-psk-123456") || strings.Contains(list.Settings, "psk") {
		t.Fatalf("list leaked PSK: %s", list.Settings)
	}
	detail := &model.Inbound{Protocol: model.Snell, Settings: `{"psk":"secret-psk-123456"}`}
	if !strings.Contains(detail.Settings, "secret-psk-123456") {
		t.Fatal("detail must retain PSK")
	}
}

func TestUpdateSnellRejectsStoredRemoteInboundWhenRequestOmitsNodeID(t *testing.T) {
	setupConflictDB(t)
	node := &model.Node{Name: "snell-remote", Address: "127.0.0.1", Port: 2096, ApiToken: "token", Enable: true}
	if err := database.GetDB().Create(node).Error; err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{Tag: "snell-remote", Enable: false, Port: 443, Protocol: model.Snell, Settings: `{"psk":"valid-psk-12345678"}`, NodeID: &node.Id}
	if err := database.GetDB().Create(inbound).Error; err != nil {
		t.Fatal(err)
	}
	update := *inbound
	update.NodeID = nil
	if _, _, err := (&InboundService{}).UpdateInbound(&update); err == nil {
		t.Fatal("remote Snell update must be rejected even when nodeId is omitted")
	}
}
