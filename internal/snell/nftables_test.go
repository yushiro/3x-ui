package snell

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeNft struct {
	calls  [][]string
	output []byte
	err    error
}

func (f *fakeNft) Run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	return f.output, f.err
}

func (f *fakeNft) joined() string {
	parts := make([]string, 0, len(f.calls))
	for _, call := range f.calls {
		parts = append(parts, strings.Join(call, " "))
	}
	return strings.Join(parts, " ")
}

func TestNftRulesAreAbsoluteAndPrivate(t *testing.T) {
	f := &fakeNft{output: []byte(`{"nftables":[]}`)}
	m := &NftManager{Exec: f}
	if err := m.EnsureInbound(context.Background(), 7, 443, 100, 200); err != nil {
		t.Fatal(err)
	}
	joined := f.joined()
	for _, want := range []string{"inet xui_snell", "snell_7_up", "snell_7_down", "443", "tcp", "udp", "dport", "sport", "bytes 100", "bytes 200"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s from %s", want, joined)
		}
	}
	if strings.Contains(joined, "flush") || strings.Contains(joined, "table ip ") || strings.Contains(joined, "accept") || strings.Contains(joined, "drop") {
		t.Fatalf("unsafe firewall command: %s", joined)
	}
}

func TestNftRulesSplitInboundAndOutboundHooks(t *testing.T) {
	f := &fakeNft{output: []byte(`{"nftables":[]}`)}
	m := &NftManager{Exec: f}
	if err := m.EnsureInbound(context.Background(), 8, 8443, 0, 0); err != nil {
		t.Fatal(err)
	}
	joined := f.joined()
	for _, want := range []string{
		"chain inet xui_snell snell_input { type filter hook input priority 0; }",
		"chain inet xui_snell snell_output { type filter hook output priority 0; }",
		"snell_input tcp dport 8443 counter name snell_8_up",
		"snell_input udp dport 8443 counter name snell_8_up",
		"snell_output tcp sport 8443 counter name snell_8_down",
		"snell_output udp sport 8443 counter name snell_8_down",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing managed directional rule %q in %s", want, joined)
		}
	}
	if strings.Contains(joined, "snell_input tcp sport 8443") || strings.Contains(joined, "snell_input udp sport 8443") {
		t.Fatalf("downstream rules must not remain in input hook: %s", joined)
	}
}

type diagnosticNft struct{ calls [][]string }

func (f *diagnosticNft) Run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	joined := strings.Join(args, " ")
	switch {
	case strings.HasPrefix(joined, "add table "), strings.HasPrefix(joined, "add chain "):
		return []byte("Error: Could not process rule: File exists"), errors.New("exit status 1")
	case strings.HasPrefix(joined, "delete counter "):
		return []byte("Error: No such file or directory"), errors.New("exit status 1")
	case strings.HasPrefix(joined, "-j list counters "), strings.HasPrefix(joined, "-j -a list chain "):
		return []byte(`{"nftables":[]}`), nil
	}
	return nil, nil
}

func TestNftIdempotencyUsesNftDiagnosticOutput(t *testing.T) {
	f := &diagnosticNft{}
	m := &NftManager{Exec: f}
	if err := m.EnsureInbound(context.Background(), 9, 443, 0, 0); err != nil {
		t.Fatalf("duplicate table/chain diagnostics must be idempotent: %v", err)
	}
	if err := m.RemoveInbound(context.Background(), 9); err != nil {
		t.Fatalf("missing counter diagnostic must be idempotent: %v", err)
	}
}

func TestCounterReadAndListUseAbsoluteValues(t *testing.T) {
	f := &fakeNft{output: []byte(`{"nftables":[{"counter":{"name":"snell_7_up","bytes":101}},{"counter":{"name":"snell_7_down","bytes":202}}]}`)}
	m := &NftManager{Exec: f}
	got, err := m.Read(context.Background(), 7)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != (Counters{UpBytes: 101, DownBytes: 202}) {
		t.Fatalf("Read = %+v", got)
	}
	all, err := m.ListManaged(context.Background())
	if err != nil || all[7] != got {
		t.Fatalf("ListManaged = %+v, %v", all, err)
	}
}

func TestNftRepairLowerCountersAndRejectsUnsafeInputs(t *testing.T) {
	f := &fakeNft{output: []byte(`{"nftables":[{"counter":{"name":"snell_7_up","bytes":99}},{"counter":{"name":"snell_7_down","bytes":200}}]}`)}
	m := &NftManager{Exec: f}
	if err := m.EnsureInbound(context.Background(), 7, 443, 100, 200); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.joined(), "bytes 100") {
		t.Fatalf("lower counter was not repaired: %s", f.joined())
	}

	for _, id := range []int{0, -1} {
		if _, _, err := CounterNames(id); err == nil {
			t.Fatalf("CounterNames accepted unsafe id %d", id)
		}
	}
	before := len(f.calls)
	if err := m.EnsureInbound(context.Background(), 1, 0, 0, 0); err == nil {
		t.Fatal("invalid port accepted")
	}
	if len(f.calls) != before {
		t.Fatal("invalid input ran nft commands")
	}
}
