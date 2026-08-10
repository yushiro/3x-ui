package snell

import (
	"context"
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
