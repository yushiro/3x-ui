package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/mtproto"
	"github.com/mhsanaei/3x-ui/v3/internal/snell"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type LocalDeps struct {
	APIPort        func() int
	SetNeedRestart func()
	Snell          *snell.Manager
}

type Local struct {
	deps LocalDeps
	mu   sync.Mutex
}

func NewLocal(deps LocalDeps) *Local {
	return &Local{deps: deps}
}

func (l *Local) Name() string { return "local" }

// SnellStatus exposes the injected local sidecar manager without adding it to
// the Runtime interface used by remote panels.
func (l *Local) SnellStatus(id int) snell.Status {
	if l.deps.Snell == nil {
		return snell.Status{}
	}
	return l.deps.Snell.Status(id)
}

// ResetSnellTraffic resets only the named sidecar counters for one inbound.
func (l *Local) ResetSnellTraffic(ctx context.Context, id int) error {
	if l.deps.Snell == nil {
		return errors.New("Snell runtime is unavailable")
	}
	return l.deps.Snell.ResetTraffic(ctx, id)
}

// BeginSnellLifecycle keeps a complete service-level transition for one
// inbound together, including its database update and zero-seed restart.
func (l *Local) BeginSnellLifecycle(ctx context.Context, id int) (context.Context, func(), error) {
	if l.deps.Snell == nil {
		return ctx, nil, errors.New("Snell runtime is unavailable")
	}
	return l.deps.Snell.BeginLifecycle(ctx, id)
}

// CheckSnellHost validates Host-only prerequisites before an enabled CRUD
// transition is committed.
func (l *Local) CheckSnellHost(ctx context.Context) error {
	if l.deps.Snell == nil {
		return errors.New("Snell runtime is unavailable")
	}
	return l.deps.Snell.CheckHost(ctx)
}

// StopSnell stops one sidecar without deleting its owned counter objects.
func (l *Local) StopSnell(ctx context.Context, id int) error {
	if l.deps.Snell == nil {
		return errors.New("Snell runtime is unavailable")
	}
	return l.deps.Snell.Stop(ctx, id, true)
}

// ReconcileSnell converges the local sidecar set without affecting Xray.
func (l *Local) ReconcileSnell(ctx context.Context, desired []snell.Instance) error {
	if l.deps.Snell == nil {
		return errors.New("Snell runtime is unavailable")
	}
	return l.deps.Snell.Reconcile(ctx, desired)
}

// ReadSnellCounters returns absolute nft totals for one sidecar.
func (l *Local) ReadSnellCounters(ctx context.Context, id int) (snell.Counters, error) {
	if l.deps.Snell == nil {
		return snell.Counters{}, errors.New("Snell runtime is unavailable")
	}
	return l.deps.Snell.ReadTraffic(ctx, id)
}

func (l *Local) withAPI(fn func(api *xray.XrayAPI) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	port := l.deps.APIPort()
	if port <= 0 {
		return errors.New("local xray is not running")
	}
	var api xray.XrayAPI
	if err := api.Init(port); err != nil {
		return err
	}
	defer api.Close()
	return fn(&api)
}

func (l *Local) AddInbound(ctx context.Context, ib *model.Inbound) error {
	if ib.Protocol == model.Snell {
		return l.ensureSnell(ctx, ib)
	}
	if ib.Protocol == model.MTProto {
		inst, ok := mtproto.InstanceFromInbound(ib)
		if !ok {
			return nil
		}
		return mtproto.GetManager().Ensure(inst)
	}
	body, err := json.MarshalIndent(ib.GenXrayInboundConfig(), "", "  ")
	if err != nil {
		return err
	}
	return l.withAPI(func(api *xray.XrayAPI) error {
		return api.AddInbound(body)
	})
}

func (l *Local) DelInbound(ctx context.Context, ib *model.Inbound) error {
	if ib.Protocol == model.Snell {
		if l.deps.Snell == nil {
			return errors.New("Snell runtime is unavailable")
		}
		return l.deps.Snell.Remove(ctx, ib.Id)
	}
	if ib.Protocol == model.MTProto {
		mtproto.GetManager().Remove(ib.Id)
		return nil
	}
	return l.withAPI(func(api *xray.XrayAPI) error {
		return api.DelInbound(ib.Tag)
	})
}

func (l *Local) UpdateInbound(ctx context.Context, oldIb, newIb *model.Inbound) error {
	if oldIb.Protocol == model.Snell || newIb.Protocol == model.Snell {
		return l.updateSnellInbound(ctx, oldIb, newIb)
	}
	if oldIb.Protocol == model.MTProto || newIb.Protocol == model.MTProto {
		return l.updateMtprotoInbound(ctx, oldIb, newIb)
	}
	_ = l.DelInbound(ctx, oldIb)
	if !newIb.Enable {
		return nil
	}
	return l.AddInbound(ctx, newIb)
}

func (l *Local) ensureSnell(ctx context.Context, ib *model.Inbound) error {
	if l.deps.Snell == nil {
		return errors.New("Snell runtime is unavailable")
	}
	instance, err := snell.InstanceFromInbound(ib)
	if err != nil {
		return err
	}
	return l.deps.Snell.Ensure(ctx, instance)
}

func (l *Local) updateSnellInbound(ctx context.Context, oldIb, newIb *model.Inbound) error {
	if oldIb.Protocol == model.Snell && newIb.Protocol != model.Snell {
		if l.deps.Snell == nil {
			return errors.New("Snell runtime is unavailable")
		}
		if err := l.deps.Snell.Remove(ctx, oldIb.Id); err != nil {
			return err
		}
		if !newIb.Enable {
			return nil
		}
		return l.AddInbound(ctx, newIb)
	}
	if oldIb.Protocol != model.Snell {
		_ = l.DelInbound(ctx, oldIb)
	}
	return l.ensureSnell(ctx, newIb)
}

// updateMtprotoInbound applies an inbound update without the Del+Add sequence
// the xray path uses: Remove would drop the manager's fingerprint state, which
// is what lets Ensure keep the running mtg process (and its live connections)
// when nothing in the generated config changed. The sidecar is only stopped
// when the inbound is disabled, loses its last active secret, or moves to a
// different protocol.
func (l *Local) updateMtprotoInbound(ctx context.Context, oldIb, newIb *model.Inbound) error {
	if oldIb.Protocol == model.MTProto && newIb.Protocol != model.MTProto {
		mtproto.GetManager().Remove(oldIb.Id)
		if !newIb.Enable {
			return nil
		}
		return l.AddInbound(ctx, newIb)
	}
	if oldIb.Protocol != model.MTProto {
		_ = l.DelInbound(ctx, oldIb)
	}
	if !newIb.Enable {
		mtproto.GetManager().Remove(newIb.Id)
		return nil
	}
	inst, ok := mtproto.InstanceFromInbound(newIb)
	if !ok {
		mtproto.GetManager().Remove(newIb.Id)
		return nil
	}
	return mtproto.GetManager().Ensure(inst)
}

func (l *Local) AddUser(_ context.Context, ib *model.Inbound, userMap map[string]any) error {
	if ib.Protocol == model.MTProto || ib.Protocol == model.Snell {
		return nil
	}
	return l.withAPI(func(api *xray.XrayAPI) error {
		return api.AddUser(string(ib.Protocol), ib.Tag, userMap)
	})
}

func (l *Local) RemoveUser(_ context.Context, ib *model.Inbound, email string) error {
	if ib.Protocol == model.MTProto || ib.Protocol == model.Snell {
		return nil
	}
	return l.withAPI(func(api *xray.XrayAPI) error {
		return api.RemoveUser(ib.Tag, email)
	})
}

func (l *Local) AddClient(ctx context.Context, ib *model.Inbound, client model.Client) error {
	if !client.Enable {
		return nil
	}
	user := map[string]any{
		"email":        client.Email,
		"id":           client.ID,
		"security":     client.Security,
		"flow":         client.Flow,
		"auth":         client.Auth,
		"password":     client.Password,
		"publicKey":    client.PublicKey,
		"allowedIPs":   client.AllowedIPs,
		"preSharedKey": client.PreSharedKey,
		"keepAlive":    wgKeepAlive(client.KeepAlive),
	}
	return l.AddUser(ctx, ib, user)
}

func (l *Local) DeleteUser(ctx context.Context, ib *model.Inbound, email string) error {
	if email == "" {
		return nil
	}
	if err := l.RemoveUser(ctx, ib, email); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil
		}
		return err
	}
	return nil
}

func (l *Local) DeleteClient(context.Context, string) error {
	return nil
}

func (l *Local) UpdateUser(ctx context.Context, ib *model.Inbound, oldEmail string, payload model.Client) error {
	if oldEmail != "" {
		if err := l.RemoveUser(ctx, ib, oldEmail); err != nil && !strings.Contains(err.Error(), "not found") {
			return err
		}
	}
	if !payload.Enable {
		return nil
	}
	user := map[string]any{
		"email":        payload.Email,
		"id":           payload.ID,
		"security":     payload.Security,
		"flow":         payload.Flow,
		"auth":         payload.Auth,
		"password":     payload.Password,
		"publicKey":    payload.PublicKey,
		"allowedIPs":   payload.AllowedIPs,
		"preSharedKey": payload.PreSharedKey,
		"keepAlive":    wgKeepAlive(payload.KeepAlive),
	}
	return l.AddUser(ctx, ib, user)
}

func wgKeepAlive(seconds int) string {
	if seconds <= 0 {
		return ""
	}
	return strconv.Itoa(seconds)
}

func (l *Local) RestartXray(_ context.Context) error {
	if l.deps.SetNeedRestart != nil {
		l.deps.SetNeedRestart()
	}
	return nil
}

func (l *Local) ResetClientTraffic(_ context.Context, _ *model.Inbound, _ string) error {
	return nil
}

func (l *Local) ResetAllTraffics(_ context.Context) error {
	return nil
}

func (l *Local) ResetInboundTraffic(_ context.Context, _ *model.Inbound) error {
	return nil
}
