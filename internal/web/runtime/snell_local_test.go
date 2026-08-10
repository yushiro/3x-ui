package runtime

import (
	"context"
	"sync"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/snell"
)

type localSnellProcess struct{ running bool }

func (p *localSnellProcess) Stop(context.Context) error { p.running = false; return nil }
func (p *localSnellProcess) Wait() error                { select {} }
func (p *localSnellProcess) Running() bool              { return p.running }

type localSnellLauncher struct{ starts int }

func (f *localSnellLauncher) Start(context.Context, string, string) (snell.ManagedProcess, error) {
	f.starts++
	return &localSnellProcess{running: true}, nil
}

type localSnellHost struct{}

func (localSnellHost) Check(context.Context) error { return nil }

type localSnellNft struct{ mu sync.Mutex }

func (f *localSnellNft) Run(context.Context, ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return []byte(`{"nftables":[]}`), nil
}

func TestLocalSnellDispatchesWithoutXray(t *testing.T) {
	launch := &localSnellLauncher{}
	m := snell.NewManager(launch, localSnellHost{}, &snell.NftManager{Exec: &localSnellNft{}}, "/bin/snell-server", t.TempDir())
	local := NewLocal(LocalDeps{Snell: m, APIPort: func() int { return 0 }})
	ib := &model.Inbound{Id: 17, Enable: true, Listen: "0.0.0.0", Port: 443, Protocol: model.Snell, Settings: `{"psk":"valid-psk-12345678"}`}
	if err := local.AddInbound(context.Background(), ib); err != nil {
		t.Fatalf("AddInbound: %v", err)
	}
	if launch.starts != 1 {
		t.Fatalf("Snell launch count = %d", launch.starts)
	}
	if err := local.DelInbound(context.Background(), ib); err != nil {
		t.Fatalf("DelInbound: %v", err)
	}
}
