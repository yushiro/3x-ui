package snell

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	nftFamily = "inet"
	nftTable  = "xui_snell"
	nftChain  = "snell_input"
)

// Counters holds absolute byte totals reported by nftables.
type Counters struct {
	UpBytes   int64
	DownBytes int64
}

// NftExecutor is the nft command boundary. All arguments are passed directly,
// never through a shell.
type NftExecutor interface {
	Run(context.Context, ...string) ([]byte, error)
}

// NftManager owns only the dedicated inet xui_snell table.
type NftManager struct {
	Exec NftExecutor
}

// HostChecker is the small boundary used by callers before they manage Snell.
type HostChecker interface {
	Check(context.Context) error
}

// CounterNames returns the private nftables object names for one inbound.
func CounterNames(id int) (string, string, error) {
	if id <= 0 {
		return "", "", fmt.Errorf("invalid Snell inbound id")
	}
	return fmt.Sprintf("snell_%d_up", id), fmt.Sprintf("snell_%d_down", id), nil
}

// EnsureInbound makes one inbound's named counters and TCP/UDP accounting
// rules present. Counters at or above their database seed are preserved.
func (m *NftManager) EnsureInbound(ctx context.Context, id, port int, up, down int64) error {
	upName, downName, err := CounterNames(id)
	if err != nil {
		return err
	}
	if port < 1 || port > 65535 || up < 0 || down < 0 {
		return fmt.Errorf("invalid Snell counter seed")
	}
	if err := m.ensureBase(ctx); err != nil {
		return err
	}

	current, err := m.listCounterValues(ctx)
	if err != nil {
		return err
	}
	currentUp, hasUp := current[upName]
	currentDown, hasDown := current[downName]
	if hasUp && currentUp >= up && hasDown && currentDown >= down {
		return nil
	}

	if err := m.removeInboundRules(ctx, upName, downName); err != nil {
		return err
	}
	if !hasUp || currentUp < up {
		if err := m.replaceCounter(ctx, upName, up); err != nil {
			return err
		}
	}
	if !hasDown || currentDown < down {
		if err := m.replaceCounter(ctx, downName, down); err != nil {
			return err
		}
	}
	return m.addInboundRules(ctx, port, upName, downName)
}

// Read returns both absolute counters for one managed inbound.
func (m *NftManager) Read(ctx context.Context, id int) (Counters, error) {
	upName, downName, err := CounterNames(id)
	if err != nil {
		return Counters{}, err
	}
	values, err := m.listCounterValues(ctx)
	if err != nil {
		return Counters{}, err
	}
	up, hasUp := values[upName]
	down, hasDown := values[downName]
	if !hasUp || !hasDown {
		return Counters{}, fmt.Errorf("Snell counters for inbound %d not found", id)
	}
	return Counters{UpBytes: up, DownBytes: down}, nil
}

// ListManaged returns complete managed counter pairs keyed by inbound ID.
func (m *NftManager) ListManaged(ctx context.Context) (map[int]Counters, error) {
	values, err := m.listCounterValues(ctx)
	if err != nil {
		return nil, err
	}
	result := make(map[int]Counters)
	for name, value := range values {
		id, direction, ok := parseCounterName(name)
		if !ok {
			continue
		}
		counter := result[id]
		if direction == "up" {
			counter.UpBytes = value
		} else {
			counter.DownBytes = value
		}
		result[id] = counter
	}
	for id := range result {
		up, down, _ := CounterNames(id)
		if _, ok := values[up]; !ok {
			delete(result, id)
			continue
		}
		if _, ok := values[down]; !ok {
			delete(result, id)
		}
	}
	return result, nil
}

// RemoveInbound deletes only the named rules and counters for one inbound.
func (m *NftManager) RemoveInbound(ctx context.Context, id int) error {
	upName, downName, err := CounterNames(id)
	if err != nil {
		return err
	}
	if err := m.removeInboundRules(ctx, upName, downName); err != nil {
		return err
	}
	if err := m.runIgnoreMissing(ctx, "delete", "counter", nftFamily, nftTable, upName); err != nil {
		return err
	}
	return m.runIgnoreMissing(ctx, "delete", "counter", nftFamily, nftTable, downName)
}

// ResetInbound resets only the two named counters for one inbound.
func (m *NftManager) ResetInbound(ctx context.Context, id int) error {
	upName, downName, err := CounterNames(id)
	if err != nil {
		return err
	}
	if _, err := m.run(ctx, "reset", "counter", nftFamily, nftTable, upName); err != nil {
		return err
	}
	_, err = m.run(ctx, "reset", "counter", nftFamily, nftTable, downName)
	return err
}

func (m *NftManager) ensureBase(ctx context.Context) error {
	if err := m.runIgnoreExists(ctx, "add", "table", nftFamily, nftTable); err != nil {
		return err
	}
	return m.runIgnoreExists(ctx, "add", "chain", nftFamily, nftTable, nftChain, "{", "type", "filter", "hook", "input", "priority", "0;", "}")
}

func (m *NftManager) replaceCounter(ctx context.Context, name string, seed int64) error {
	if err := m.runIgnoreMissing(ctx, "delete", "counter", nftFamily, nftTable, name); err != nil {
		return err
	}
	_, err := m.run(ctx, "add", "counter", nftFamily, nftTable, name, "{", "packets", "0", "bytes", strconv.FormatInt(seed, 10), "}")
	return err
}

func (m *NftManager) addInboundRules(ctx context.Context, port int, upName, downName string) error {
	portText := strconv.Itoa(port)
	upComment := strconv.Quote(upName)
	downComment := strconv.Quote(downName)
	rules := [][]string{
		{"add", "rule", nftFamily, nftTable, nftChain, "tcp", "dport", portText, "counter", "name", upName, "comment", upComment},
		{"add", "rule", nftFamily, nftTable, nftChain, "udp", "dport", portText, "counter", "name", upName, "comment", upComment},
		{"add", "rule", nftFamily, nftTable, nftChain, "tcp", "sport", portText, "counter", "name", downName, "comment", downComment},
		{"add", "rule", nftFamily, nftTable, nftChain, "udp", "sport", portText, "counter", "name", downName, "comment", downComment},
	}
	for _, rule := range rules {
		if _, err := m.run(ctx, rule...); err != nil {
			return err
		}
	}
	return nil
}

func (m *NftManager) removeInboundRules(ctx context.Context, names ...string) error {
	handles, err := m.ruleHandles(ctx, names...)
	if err != nil {
		return err
	}
	for _, handle := range handles {
		if _, err := m.run(ctx, "delete", "rule", nftFamily, nftTable, nftChain, "handle", strconv.FormatInt(handle, 10)); err != nil {
			return err
		}
	}
	return nil
}

func (m *NftManager) listCounterValues(ctx context.Context) (map[string]int64, error) {
	output, err := m.run(ctx, "-j", "list", "counters", "table", nftFamily, nftTable)
	if err != nil {
		return nil, err
	}
	return parseCounterValues(output)
}

func (m *NftManager) ruleHandles(ctx context.Context, names ...string) ([]int64, error) {
	output, err := m.run(ctx, "-j", "-a", "list", "chain", nftFamily, nftTable, nftChain)
	if err != nil {
		return nil, err
	}
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[name] = true
	}
	var document any
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.UseNumber()
	if len(bytes.TrimSpace(output)) == 0 || decoder.Decode(&document) != nil {
		return nil, nil
	}
	handles := make([]int64, 0)
	collectRuleHandles(document, wanted, &handles)
	sort.Slice(handles, func(i, j int) bool { return handles[i] > handles[j] })
	return handles, nil
}

func (m *NftManager) run(ctx context.Context, args ...string) ([]byte, error) {
	if m == nil || m.Exec == nil {
		return nil, fmt.Errorf("nft executor is unavailable")
	}
	return m.Exec.Run(ctx, args...)
}

func (m *NftManager) runIgnoreExists(ctx context.Context, args ...string) error {
	_, err := m.run(ctx, args...)
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "file exists") {
		return err
	}
	return nil
}

func (m *NftManager) runIgnoreMissing(ctx context.Context, args ...string) error {
	_, err := m.run(ctx, args...)
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "no such file") {
		return err
	}
	return nil
}

var counterNamePattern = regexp.MustCompile(`^snell_([1-9][0-9]*)_(up|down)$`)

func parseCounterName(name string) (int, string, bool) {
	matches := counterNamePattern.FindStringSubmatch(name)
	if len(matches) != 3 {
		return 0, "", false
	}
	id, err := strconv.Atoi(matches[1])
	if err != nil || id <= 0 {
		return 0, "", false
	}
	return id, matches[2], true
}

func parseCounterValues(output []byte) (map[string]int64, error) {
	values := make(map[string]int64)
	trimmed := bytes.TrimSpace(output)
	if len(trimmed) == 0 {
		return values, nil
	}
	var document any
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err == nil {
		collectCounterValues(document, values)
		return values, nil
	}

	plainCounter := regexp.MustCompile(`(?s)counter\\s+(snell_[1-9][0-9]*_(?:up|down))\\s*\\{.*?bytes\\s+([0-9]+)`)
	for _, matches := range plainCounter.FindAllStringSubmatch(string(trimmed), -1) {
		value, err := strconv.ParseInt(matches[2], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid nft counter value")
		}
		values[matches[1]] = value
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("invalid nft counter output")
	}
	return values, nil
}

func collectCounterValues(value any, output map[string]int64) {
	switch value := value.(type) {
	case map[string]any:
		if counter, ok := value["counter"].(map[string]any); ok {
			name, _ := counter["name"].(string)
			if _, _, valid := parseCounterName(name); valid {
				if bytesValue, ok := jsonNumber(counter["bytes"]); ok {
					output[name] = bytesValue
				}
			}
		}
		for _, child := range value {
			collectCounterValues(child, output)
		}
	case []any:
		for _, child := range value {
			collectCounterValues(child, output)
		}
	}
}

func collectRuleHandles(value any, wanted map[string]bool, handles *[]int64) {
	switch value := value.(type) {
	case map[string]any:
		if rule, ok := value["rule"].(map[string]any); ok {
			comment, _ := rule["comment"].(string)
			if wanted[comment] {
				if handle, ok := jsonNumber(rule["handle"]); ok {
					*handles = append(*handles, handle)
				}
			}
		}
		for _, child := range value {
			collectRuleHandles(child, wanted, handles)
		}
	case []any:
		for _, child := range value {
			collectRuleHandles(child, wanted, handles)
		}
	}
}

func jsonNumber(value any) (int64, bool) {
	switch value := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseInt(string(value), 10, 64)
		return parsed, err == nil && parsed >= 0
	case float64:
		return int64(value), value >= 0 && value == float64(int64(value))
	default:
		return 0, false
	}
}
