# Snell v5.0.1 Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 Linux Host 上将 Snell Server v5.0.1 作为不经过 Xray 的独立 sidecar 入站接入 3x-ui，提供安全 PSK、TCP/UDP nftables 绝对计数、共享月度配额、恢复和唯一 Surge v5 配置复制输出。

**Architecture:** 安装/升级脚本在有网络和 root 权限时下载并验证 Snell ZIP，原子安装到 `bin/snell/snell-server`；Go 运行时只验证 Linux、架构、文件权限、nft 可用性和规则能力，绝不下载。`internal/snell` 提供 settings、配置、参数化进程、nftables 和按 ID 串行的 manager；`runtime.Local` 在现有 `runtime.Runtime` dispatch 中调用它，`InboundService` 负责数据库语义，`SnellJob` 负责对账/配额，前端复用既有入站弹窗。

**Tech Stack:** Go、GORM、`os/exec`、Linux nftables CLI、Bash、React 19、TypeScript、React Hook Form、Zod、Ant Design、Go test、Vitest。

## Global Constraints

- 仅支持 Linux Host；Docker、Kubernetes、macOS、Windows、容器 relay、用户态代理、v6 和未列出的架构拒绝启动 Snell。
- 版本固定 v5.0.1，四个 URL 固定为 `https://dl.nssurge.com/snell/snell-server-v5.0.1-linux-amd64.zip`、`https://dl.nssurge.com/snell/snell-server-v5.0.1-linux-i386.zip`、`https://dl.nssurge.com/snell/snell-server-v5.0.1-linux-aarch64.zip`、`https://dl.nssurge.com/snell/snell-server-v5.0.1-linux-armv7l.zip`；运行时禁止下载。
- 不新增数据库迁移、`ClientTraffic`、设备级统计、`disableReason`、`quotaDisabled`、Snell v6 或非 Surge 输出。
- nftables 只管理 `inet xui_snell`、自己的链/规则/计数器，绝不 flush 或修改其他表，不作 verdict。
- 配置路径 `bin/snell/config/snell-<id>.conf`，模式 `0600`；PSK 不进入列表 API、日志、订阅或错误文本。
- 所有生命周期、采集、重置、配额和退出回调按入站 ID 串行化；错误只隔离当前 Snell 入站。
- 测试通过 fake downloader/script runner/process/nft executor/clock；不需要公共网络、特权 nftables 或真实 snell-server。
- 完成边界是源码和本地验证，不包含发布、部署或提交二进制。

## Locked File Structure

- `internal/database/model/model.go`, `internal/database/model/snell.go`, `internal/database/model/snell_test.go`: 协议常量、settings、PSK、脱敏和非持久运行状态 DTO。
- `internal/snell/settings.go`, `config.go`, `prerequisites.go`, `process.go`, `nftables.go`, `manager.go` 及对应测试：运行时组件；无安装下载器。
- `install.sh`, `update.sh`: 下载、固定摘要、ZIP 安全检查、原子安装；`deploy/test/smoke-noninteractive.sh`: 脚本静态/非网络回归。
- `internal/web/runtime/runtime.go`, `local.go`, `manager.go`, `internal/web/web.go`: 复用既有 runtime dispatch 和本地 runtime 构造。
- `internal/web/service/inbound.go`, `inbound_traffic.go`, `inbound_disable.go`, `inbound_snell.go` 及精确测试：CRUD、授权脱敏、生命周期、绝对流量。
- `internal/web/job/snell_job.go`, `periodic_traffic_reset_job.go` 及测试：对账、配额、月度/手动重置。
- `frontend/src/schemas/primitives/protocol.ts`, `schemas/protocols/inbound/{index,snell}.ts`, `schemas/forms/inbound-form.ts`, `lib/xray/{inbound-defaults,inbound-form-adapter}.ts`: schema/default/wire adapter。
- `frontend/src/pages/inbounds/form/{InboundFormModal.tsx,protocols/{index,snell}.tsx}`, `info/*`, `list/*`, `qr/QrCodeModal.tsx`, `lib/xray/inbound-link.ts`: 表单、状态、Surge 复制和排除。
- `frontend/src/test/snell-*.test.ts(x)`, `internal/sub/*_test.go`, `README.md`: 聚焦回归、订阅排除、Host 文档。

### Task 1: Freeze and audit the four installer digests

**Files:**
- Create: `scripts/snell-v5.0.1-sha256.txt`
- Modify: `install.sh`, `update.sh`, `deploy/test/smoke-noninteractive.sh`
- Test: `deploy/test/smoke-noninteractive.sh`

**Interfaces:**
- `scripts/snell-v5.0.1-sha256.txt` contains four lines `<sha256>  <exact-url>`; scripts expose `snell_artifact_for_arch`, `snell_download_and_install`, and `snell_verify_zip` with arguments `(url, expected_sha, destination_root)`.
- This is the only network-dependent step: before implementation, a maintainer downloads each exact official URL once, computes `sha256sum`, and records the four digests after a second maintainer or the official release source verifies them. Do not invent a digest when the official page has no digest listing.

- [ ] **Step 1: Obtain and audit the four archives.** Run `curl -fL <URL> -o /private/tmp/<name>.zip` for each URL, run `sha256sum`, inspect ZIP members with `unzip -Z1`, and record URL, version, architecture, digest, and member inventory in the commit/review evidence before adding the four literal lines. If the official release page later publishes digests, compare them as a second source; never invent a missing digest.
- [ ] **Step 2: Write deterministic shell tests first.** Extend `deploy/test/smoke-noninteractive.sh` with a fake `curl`, `sha256sum`, `unzip`, and `install` in a temporary PATH; assert `amd64/386/arm64/armv7` mapping, no `latest`, digest mismatch failure, `../escape` rejection, extra-file rejection, and unchanged destination after failed replacement.
- [ ] **Step 3: Run the failing script checks.** Run `bash -n install.sh update.sh deploy/test/smoke-noninteractive.sh`; the new fake assertions fail before helper functions exist.
- [ ] **Step 4: Implement install/upgrade helpers.** Add an immutable case mapping, verify ZIP bytes before extraction, allow exactly one regular `snell-server` member, reject absolute/`..` names and symlink entries, extract to a same-filesystem temporary directory, chmod `0755`, and `mv` the verified executable atomically into `bin/snell/snell-server`; neither script may leave a partial target.

```bash
snell_artifact_for_arch() {
  case "$1" in amd64) echo "$SNELL_AMD64_URL $SNELL_AMD64_SHA256";; 386) echo "$SNELL_386_URL $SNELL_386_SHA256";; arm64) echo "$SNELL_ARM64_URL $SNELL_ARM64_SHA256";; armv7) echo "$SNELL_ARMV7_URL $SNELL_ARMV7_SHA256";; *) return 2;; esac
}
snell_verify_zip() { test "$(sha256sum "$1" | awk '{print $1}')" = "$2" || return 1; unzip -Z1 "$1" | awk 'END { exit !(NR == 1 && $1 == "snell-server") }'; }
```

```bash
SNELL_ARCH=$(arch) || exit 1
read -r SNELL_URL SNELL_SHA256 < <(snell_artifact_for_arch "$SNELL_ARCH") || exit 1
snell_download_and_install "$SNELL_URL" "$SNELL_SHA256" "$xui_folder/bin/snell"
```
- [ ] **Step 5: Run script checks.** Run `bash -n install.sh update.sh deploy/test/smoke-noninteractive.sh` and `bash deploy/test/smoke-noninteractive.sh`; all fake tests pass without network.
- [ ] **Step 6: Commit.** `git add scripts/snell-v5.0.1-sha256.txt install.sh update.sh deploy/test/smoke-noninteractive.sh && git commit -m "feat: install verified Snell v5.0.1"`.

### Task 2: Register Snell settings without a client model

**Files:**
- Modify: `internal/database/model/model.go`
- Create: `internal/database/model/snell.go`, `internal/database/model/snell_test.go`
- Modify: `internal/web/service/inbound.go`

**Interfaces:**
- Define `const Snell Protocol = "snell"`, `type SnellSettings struct { PSK string `json:"psk"` }`, `func ParseSnellSettings(raw string) (SnellSettings, error)`, `func ValidateSnellSettings(SnellSettings) error`, `func NewSnellPSK() (string, error)`, and `func NormalizeSnellSettings(raw string, existing *SnellSettings) (string, error)`.
- `NormalizeSnellSettings` generates on create when PSK is missing/empty; on edit it copies `existing.PSK` when the submitted PSK is missing/empty, and accepts a submitted non-empty valid PSK (including one generated by the frontend button) as the replacement. No request flag or database field is added.

- [ ] **Step 1: Write failing Go tests.** Test malformed JSON, missing/invalid PSK, generated cryptographic randomness, rejection of `clients`, create default, empty edit preservation, non-empty edit replacement, protocol validation, and existing port conflict.
- [ ] **Step 2: Run failure.** `go test ./internal/database/model ./internal/web/service -run 'Snell|snell' -count=1` fails because the protocol and settings contract do not exist.
- [ ] **Step 3: Implement.** Use `crypto/rand`, encode 32 random bytes as lowercase hex, reject whitespace/control characters and lengths outside `16..128`, and add `snell` to the exact validator tag currently on `Inbound.Protocol`.

```go
func NormalizeSnellSettings(raw string, existing *SnellSettings) (string, error) {
    var in struct{ PSK string `json:"psk"` }
    if raw != "" && json.Unmarshal([]byte(raw), &in) != nil { return "", errors.New("invalid Snell settings") }
    psk := strings.TrimSpace(in.PSK)
    if psk == "" && existing != nil { psk = existing.PSK }
    if psk == "" { var err error; psk, err = NewSnellPSK(); if err != nil { return "", err } }
    if err := ValidateSnellSettings(SnellSettings{PSK: psk}); err != nil { return "", err }
    b, err := json.Marshal(SnellSettings{PSK: psk}); return string(b), err
}
```

```go
func TestNormalizeSnellSettings(t *testing.T) {
    old := &SnellSettings{PSK: "old-valid-psk-1234"}
    got, _ := NormalizeSnellSettings(`{"psk":""}`, old)
    if !strings.Contains(got, old.PSK) { t.Fatal("empty edit must preserve old PSK") }
    got, _ = NormalizeSnellSettings(`{"psk":"new-valid-psk-1234"}`, old)
    if strings.Contains(got, old.PSK) { t.Fatal("non-empty edit must replace PSK") }
}
```
- [ ] **Step 4: Run pass.** `go test ./internal/database/model ./internal/web/service -run 'Snell|snell' -count=1`.
- [ ] **Step 5: Commit.** `git add internal/database/model internal/web/service/inbound.go && git commit -m "feat: add Snell settings contract"`.

### Task 3: Build runtime configuration, Host checks, and nftables

**Files:**
- Create: `internal/snell/settings.go`, `internal/snell/config.go`, `internal/snell/prerequisites.go`, `internal/snell/nftables.go`
- Create: `internal/snell/settings_test.go`, `config_test.go`, `prerequisites_test.go`, `nftables_test.go`

**Interfaces:**
- Define `type Instance struct { ID int; Listen string; Port int; PSK string; Enable bool; Up int64; Down int64; Total int64 }`, `func RenderConfig(Instance) ([]byte,error)`, and `func WriteConfig(path string, data []byte) error`.
- Define `type CommandRunner interface { Run(context.Context, string, ...string) ([]byte,error) }`, `func CheckHost(context.Context, CommandRunner, string, string, string) error`, and `func ValidateBinary(path string) error`; parameters are `(runner, binaryPath, goos, goarch)` and no downloader is accepted.
- Define `type Counters struct { UpBytes int64; DownBytes int64 }`, `type NftExecutor interface { Run(context.Context, ...string) ([]byte,error) }`, `type NftManager struct { Exec NftExecutor }`, `type HostChecker interface { Check(context.Context) error }`, and methods `EnsureInbound(context.Context, int, int, int64, int64) error`, `Read(context.Context, int) (Counters,error)`, `ListManaged(context.Context) (map[int]Counters,error)`, `RemoveInbound(context.Context,int) error`, `ResetInbound(context.Context,int) error`.

- [ ] **Step 1: Write failing fake tests.** Assert config `[Snell Server]` with `interface`, `port`, `psk`, mode `0600`, safe numeric IDs, Host rejection for non-Linux/Docker/missing binary/missing nft/permission failure, and no download symbol or network call in the package.
- [ ] **Step 2: Write failing nft tests.** Assert `inet xui_snell`, base chain without verdict, TCP+UDP destination-port `snell_N_up`, source-port `snell_N_down`, seed bytes, absolute parsing, lower-counter repair, and absence of `flush`/other table names.
- [ ] **Step 3: Run failure.** `go test ./internal/snell -run 'Test(Config|Host|Nft|Counter)' -count=1`.
- [ ] **Step 4: Implement.** Use `exec.CommandContext` through `CommandRunner`, reject invalid IDs/ports/listeners/PSKs before commands, check `runtime.GOOS == "linux"`, require executable `bin/snell/snell-server`, and issue only parameterized nft operations. Preserve a current counter >= seed; recreate missing/lower counters with the database seed.

```go
type fakeNft struct{ calls [][]string; output []byte; err error }
func (f *fakeNft) Run(_ context.Context, args ...string) ([]byte, error) { f.calls = append(f.calls, args); return f.output, f.err }
func (m *NftManager) EnsureInbound(ctx context.Context, id, port int, up, down int64) error {
    u, d, err := CounterNames(id); if err != nil { return err }
    return m.Exec.Run(ctx, "-f", "-", "table inet xui_snell", "counter", u, "bytes", strconv.FormatInt(up,10), "counter", d, "bytes", strconv.FormatInt(down,10), "port", strconv.Itoa(port))
}
```

```go
func TestNftRulesAreAbsoluteAndPrivate(t *testing.T) {
    f := new(fakeNft); m := &NftManager{Exec:f}
    if err := m.EnsureInbound(context.Background(), 7, 443, 100, 200); err != nil { t.Fatal(err) }
    joined := strings.Join(f.calls[0], " ")
    for _, want := range []string{"inet xui_snell", "snell_7_up", "snell_7_down", "443"} { if !strings.Contains(joined, want) { t.Fatalf("missing %s", want) } }
    if strings.Contains(joined, "flush") || strings.Contains(joined, "table ip ") { t.Fatal("modified unrelated firewall state") }
}
```
- [ ] **Step 5: Run pass.** `go test ./internal/snell -run 'Test(Config|Host|Nft|Counter)' -count=1 -race`.
- [ ] **Step 6: Commit.** `git add internal/snell && git commit -m "feat: add Snell runtime prerequisites and counters"`.

### Task 4: Implement the per-inbound sidecar manager and runtime dispatch

**Files:**
- Create: `internal/snell/process.go`, `internal/snell/manager.go`, `internal/snell/process_test.go`, `manager_test.go`
- Modify: `internal/web/runtime/runtime.go`, `internal/web/runtime/local.go`, `internal/web/runtime/manager.go`, `internal/web/web.go`

**Interfaces:**
- Define `type ManagedProcess interface { Stop(context.Context) error; Wait() error; Running() bool }`, `type ProcessLauncher interface { Start(context.Context, string, string) (ManagedProcess,error) }`, `type Backoff interface { Next() time.Duration; Reset() }`, and `type Status struct { Running bool; LastError string; RestartAt time.Time }`.
- Define `type Manager struct { Launch ProcessLauncher; Nft *NftManager; BinaryPath string; ConfigDir string; Host HostChecker; mu sync.Mutex; byID map[int]*entry }`, where `entry` is `type entry struct { process ManagedProcess; instance Instance; status Status; backoff Backoff }`.
- Define `Ensure(context.Context, Instance) error`, `Stop(context.Context,int,bool) error`, `Remove(context.Context,int) error`, `Reconcile(context.Context,[]Instance) error`, `HandleExit(context.Context,int,error)`, and `Status(int) Status`.
- Define `func NewBackoff() Backoff` with delays `1s, 2s, 4s, 8s, 16s, 30s, 60s`, capped at `60s`, and reset it after a stable running interval.
- Extend `runtime.LocalDeps` with `Snell *snell.Manager`; `Local.AddInbound`, `DelInbound`, and `UpdateInbound` branch on `model.Snell` before Xray/MTProto. `runtime.Runtime` keeps `ResetInboundTraffic(context.Context,*model.Inbound) error` unchanged.

- [ ] **Step 1: Write failing manager tests.** Test one process per ID, serialized update/stop/delete, safe `snell-server --config <path>` arguments, active config replacement, intentional/quota stop suppression, bounded crash backoff/reset, orphan matching by owned config path/argv, and no unrelated process kill.
- [ ] **Step 2: Run failure.** `go test ./internal/snell ./internal/web/runtime -run 'Test(Manager|Process|Local.*Snell|Orphan)' -count=1`.
- [ ] **Step 3: Implement manager and wiring.** In `Ensure`, call Host check, write `0600` config, ensure/reseed nft counters before start, and store status; `UpdateInbound` stops old, reads final counters, rewrites config/rules with seed, and starts when the row is enabled and under quota. Construct one manager in `internal/web/web.go` beside `runtime.NewManager`, pass it through `runtime.LocalDeps`, and preserve `runtime.NewManager`’s existing Local/Remote selection.

```go
type fakeLauncher struct{ starts []struct{ binary, config string }; proc *fakeProcess }
func (f *fakeLauncher) Start(_ context.Context, binary, config string) (ManagedProcess, error) { f.starts = append(f.starts, struct{binary,config string}{binary,config}); return f.proc, nil }
func NewManager(launch ProcessLauncher, host HostChecker, nft *NftManager, binary, configDir string) *Manager {
    return &Manager{Launch: launch, Host: host, Nft: nft, BinaryPath: binary, ConfigDir: configDir, byID: map[int]*entry{}}
}
func (m *Manager) Ensure(ctx context.Context, in Instance) error {
    m.mu.Lock(); defer m.mu.Unlock()
    if err := m.Host.Check(ctx); err != nil { return err }
    data, err := RenderConfig(in); if err != nil { return err }
    path := filepath.Join(m.ConfigDir, fmt.Sprintf("snell-%d.conf", in.ID)); if err = WriteConfig(path, data); err != nil { return err }
    if err = m.Nft.EnsureInbound(ctx, in.ID, in.Port, in.Up, in.Down); err != nil { return err }
    proc, err := m.Launch.Start(ctx, m.BinaryPath, path); if err != nil { return err }
    m.byID[in.ID] = &entry{process: proc, instance: in, status: Status{Running:true}, backoff: NewBackoff()}; return nil
}
func (m *Manager) Stop(ctx context.Context, id int, intentional bool) error { m.mu.Lock(); defer m.mu.Unlock(); /* mark intentional before Stop */ return nil }
```

```go
func TestManagerStartsOneOwnedProcess(t *testing.T) {
    p := &fakeProcess{}; f := &fakeLauncher{proc:p}; m := NewManager(f, fakeHost{}, fakeNftManager(), "/bin/snell-server", t.TempDir())
    if err := m.Ensure(context.Background(), Instance{ID: 3, Port: 443, PSK: "valid-psk-12345678", Enable: true}); err != nil { t.Fatal(err) }
    if len(f.starts) != 1 || !strings.HasSuffix(f.starts[0].config, "snell-3.conf") { t.Fatalf("bad launch: %#v", f.starts) }
}
```
- [ ] **Step 4: Run pass.** `go test ./internal/snell ./internal/web/runtime -run 'Test(Manager|Process|Local.*Snell|Orphan)' -count=1 -race`.
- [ ] **Step 5: Commit.** `git add internal/snell internal/web/runtime internal/web/web.go && git commit -m "feat: dispatch Snell through local runtime"`.

### Task 5: Integrate service CRUD, secrecy, runtime/error status, and absolute traffic

**Files:**
- Create: `internal/web/service/inbound_snell.go`, `inbound_snell_test.go`, `inbound_traffic_snell_test.go`
- Modify: `internal/web/service/inbound.go`, `inbound_traffic.go`, `inbound_disable.go`, `internal/web/controller/inbound.go`, `internal/database/model/model.go`, `frontend/src/models/dbinbound.ts`, `frontend/src/generated/types.ts`, `frontend/src/generated/zod.ts`

**Interfaces:**
- Reuse `runtime.GetManager().RuntimeFor(inbound.NodeID)` and the existing `runtime.Runtime` interface rather than adding a second service constructor. Define `func (s *InboundService) snellRuntimeFor(*model.Inbound) (runtime.Runtime,error)` for local Snell rows, and keep `SnellManager` owned by the singleton local runtime created in `internal/web/web.go`.
- Define `func (s *InboundService) ValidateSnell(*model.Inbound, bool) error`, `func (s *InboundService) DesiredSnellInstances() ([]snell.Instance,error)`, `func (s *InboundService) ApplySnellInbound(context.Context,*model.Inbound,*model.Inbound) error`, `func (s *InboundService) SyncSnellCounters(context.Context,*model.Inbound,snell.Counters) error`, and `func (s *InboundService) ResetSnellTraffic(context.Context,int,bool) error`.
- Define the API DTO and model field exactly as follows; `GetInbounds` and `GetInboundsSlim` fill it from `snell.Manager.Status(ib.Id)`, and standard `encoding/json` uses the struct tag to emit `runtimeStatus`; no database column is added. Run `cd frontend && npm run gen` after the Go model change so `frontend/src/generated/types.ts` and `frontend/src/generated/zod.ts` carry the same shape.

```go
type InboundViewStatus struct { Running bool `json:"running"`; ErrorCategory string `json:"errorCategory,omitempty"` }
// in model.Inbound:
RuntimeStatus *InboundViewStatus `json:"runtimeStatus,omitempty" gorm:"-"`
```

- [ ] **Step 1: Write failing service tests.** Cover create-time PSK generation, empty edit PSK preservation, non-empty edit replacement, list redaction/detail access, Host/binary/nft error category, local-only desired rows, update listener/port/PSK final sync, disable/delete, absolute DB write retry, lower-counter no rollback, quota `up+down >= total`, and no `ClientTraffic` records.
- [ ] **Step 2: Run failure.** `go test ./internal/web/service -run 'Test(Snell|.*Snell.*Traffic|.*Snell.*Lifecycle)' -count=1`.
- [ ] **Step 3: Implement exact branches.** In `AddInbound`/`UpdateInbound`, normalize settings before save and call existing runtime `UpdateInbound`; in list projection replace `settings.psk` with no key; in detail leave it for the authenticated owner; map manager status to `InboundViewStatus`; add `runtimeStatus` to `DBInboundInit` and `DBInbound`; write absolute `up/down` in one transaction and never reset nft during collection; set only this inbound `enable=false` at quota.

```go
func redactSnellList(ib *model.Inbound) {
    if ib.Protocol != model.Snell { return }
    var s map[string]any; _ = json.Unmarshal([]byte(ib.Settings), &s)
    delete(s, "psk"); b, _ := json.Marshal(s); ib.Settings = string(b)
}
func (s *InboundService) SyncSnellCounters(ctx context.Context, ib *model.Inbound, c snell.Counters) error {
    return submitTrafficWrite(func() error { return database.GetDB().Model(&model.Inbound{}).Where("id = ?", ib.Id).Updates(map[string]any{"up": c.UpBytes, "down": c.DownBytes}).Error })
}
```

```go
func TestSnellListRedactsButDetailKeepsPSK(t *testing.T) {
    list := &model.Inbound{Protocol:model.Snell, Settings:`{"psk":"secret"}`}; redactSnellList(list)
    if strings.Contains(list.Settings, "secret") { t.Fatal("list leaked PSK") }
}
```
- [ ] **Step 4: Run focused and existing tests.** `go test ./internal/web/service -run 'Test(Snell|.*Snell.*Traffic|.*Snell.*Lifecycle)' -count=1` and `go test ./internal/web/service -run 'Test(Inbound|PortConflict)' -count=1`.
- [ ] **Step 5: Regenerate and verify generated API types.** Run `cd frontend && npm run gen`, then `git diff --name-only` and retain the generator-required `frontend/src/generated/types.ts` and `frontend/src/generated/zod.ts` changes while confirming no unrelated generated artifact changed; run `cd frontend && npm run typecheck`.
- [ ] **Step 6: Commit.** `git add internal/database/model/model.go frontend/src/models/dbinbound.ts frontend/src/generated/types.ts frontend/src/generated/zod.ts internal/web/service/inbound.go internal/web/service/inbound_traffic.go internal/web/service/inbound_disable.go internal/web/controller/inbound.go internal/web/service/inbound_snell.go internal/web/service/inbound_snell_test.go internal/web/service/inbound_traffic_snell_test.go && git commit -m "feat: add Snell service lifecycle"`.

### Task 6: Add reconcile job and preserve the existing reset API contract

**Files:**
- Create: `internal/web/job/snell_job.go`, `internal/web/job/snell_job_test.go`
- Modify: `internal/web/job/periodic_traffic_reset_job.go`, `periodic_traffic_reset_job_test.go`, `internal/web/web.go`, `internal/web/service/inbound_traffic.go`

**Interfaces:**
- Define `type SnellRuntimeService interface { DesiredSnellInstances() ([]snell.Instance,error); ReconcileSnell(context.Context,[]snell.Instance) error; ReadSnellCounters(context.Context,int) (snell.Counters,error); SyncSnellCounters(context.Context,int,snell.Counters) error; EnforceSnellQuota(context.Context,int) error; ResetSnellTraffic(context.Context,int,bool) error }`.
- Define `type Clock interface { Now() time.Time }`, `type SnellJob struct { Runtime SnellRuntimeService; Clock Clock }`, `func NewSnellJob(SnellRuntimeService,Clock) *SnellJob`, and `func (j *SnellJob) Run()`.
- Keep `func (s *InboundService) ResetInboundTraffic(id int) error` unchanged. Add internal `func (s *InboundService) resetInboundTrafficForPeriod(id int, monthly bool) error`; it calls `ResetSnellTraffic(ctx,id,monthly)` for Snell and the existing runtime reset for all other protocols.
- Extract the current non-Snell body into `func (s *InboundService) resetExistingInboundTraffic(id int) error`; its SQL and remote `runtime.ResetInboundTraffic` calls remain byte-for-byte equivalent.

- [ ] **Step 1: Write failing tests.** Fake two Snell rows and existing Xray/MTProto rows; test startup reconciliation, orphan cleanup, missing/lower counter stop/reseed/start, DB failure retry, quota isolation, infinite total, manual reset preserving enable, monthly reset zeroing both stores and re-enabling valid Snell, and no changed Xray/MTProto reset behavior.
- [ ] **Step 2: Run failure.** `go test ./internal/web/job -run 'Test(Snell|Periodic.*Reset)' -count=1`.
- [ ] **Step 3: Implement.** `SnellJob.Run` obtains desired rows, reconciles manager, reads absolute counters, syncs them, then enforces quota. In `PeriodicTrafficResetJob.Run`, replace only the existing service call with `resetInboundTrafficForPeriod(inbound.Id, j.period == "monthly")`; keep client reset calls and the public service method unchanged. Register `job.NewSnellJob` in `internal/web/web.go` next to `NewMtprotoJob`.

```go
func (s *InboundService) ResetInboundTraffic(id int) error { return s.resetInboundTrafficForPeriod(id, false) }
func (s *InboundService) resetInboundTrafficForPeriod(id int, monthly bool) error {
    ib, err := s.GetInbound(id); if err != nil { return err }
    if ib.Protocol == model.Snell { return s.ResetSnellTraffic(context.Background(), id, monthly) }
    return s.resetExistingInboundTraffic(id)
}
func (j *SnellJob) Run() {
    rows, err := j.Runtime.DesiredSnellInstances(); if err != nil { return }
    if err = j.Runtime.ReconcileSnell(context.Background(), rows); err != nil { return }
    for _, row := range rows { c, e := j.Runtime.ReadSnellCounters(context.Background(), row.ID); if e == nil { _ = j.Runtime.SyncSnellCounters(context.Background(), row.ID, c); _ = j.Runtime.EnforceSnellQuota(context.Background(), row.ID) } }
}
```

```go
func TestMonthlySnellResetReenables(t *testing.T) {
    fake := &fakeSnellRuntime{inbound: model.Inbound{Id: 8, Protocol:model.Snell, Enable:false, TrafficReset:"monthly"}}
    if err := fake.service.resetInboundTrafficForPeriod(8, true); err != nil { t.Fatal(err) }
    if !fake.reenabled || fake.counters != (snell.Counters{}) { t.Fatalf("reset state: %#v", fake) }
}
```
- [ ] **Step 4: Run pass.** `go test ./internal/web/job -run 'Test(Snell|Periodic.*Reset)' -count=1` and `go test ./internal/web/job -run 'Test.*Traffic' -count=1`.
- [ ] **Step 5: Commit.** `git add internal/web/job internal/web/service/inbound_traffic.go internal/web/web.go && git commit -m "feat: reconcile Snell traffic and resets"`.

### Task 7: Add schema, form behavior, and exact Surge copy

**Files:**
- Create: `frontend/src/schemas/protocols/inbound/snell.ts`, `frontend/src/pages/inbounds/form/protocols/snell.tsx`, `frontend/src/test/snell-fields.test.tsx`
- Modify: `frontend/src/schemas/primitives/protocol.ts`, `schemas/protocols/inbound/index.ts`, `schemas/forms/inbound-form.ts`, `lib/xray/inbound-defaults.ts`, `lib/xray/inbound-form-adapter.ts`, `pages/inbounds/form/protocols/index.ts`, `pages/inbounds/form/InboundFormModal.tsx`

**Interfaces:**
- Define `SnellInboundSettingsSchema = z.object({ psk: z.string().min(16) })`, `type SnellInboundSettings`, `function buildSnellSurgeLine(name:string,host:string,port:number,psk:string):string`, and `function canCopySnellSurge(host:string,port:number,psk:string):boolean`.
- `buildSnellSurgeLine` returns exactly ``${name} = snell, ${host}, ${port}, psk=${psk}, version=5``; `SnellFields` uses existing share-address resolver and only displays PSK, Generate/Regenerate, Copy Surge v5.

- [ ] **Step 1: Write failing Vitest tests.** Cover protocol union/default, create PSK, edit empty preservation, click-only regenerate, exact output, invalid host/port/PSK disabled copy, monthly day field, and hidden transport/TLS/client controls.
- [ ] **Step 2: Run failure.** `cd frontend && npm test -- --run src/test/snell-fields.test.tsx src/test/inbound-form-adapter.test.ts`.
- [ ] **Step 3: Implement.** Add `snell` to `Protocols` and discriminated settings union; add default `{psk: generate}` in `createDefaultInboundSettings`; keep empty edit PSK out of the wire payload; add a `regeneratePsk` form action that sets a new value only from the explicit button handler; gate all Xray-only tabs on `protocol !== Protocols.SNELL`.

```ts
export function buildSnellSurgeLine(name: string, host: string, port: number, psk: string): string {
  return `${name} = snell, ${host}, ${port}, psk=${psk}, version=5`;
}
export function canCopySnellSurge(host: string, port: number, psk: string): boolean {
  return host.trim() !== '' && Number.isInteger(port) && port >= 1 && port <= 65535 && psk.trim() !== '';
}
// SnellFields.tsx
const psk = useWatch({ control, name: 'settings.psk' }) as string;
return <><FormField name={['settings','psk']} label={t('pages.inbounds.form.psk')}><Input.Password /></FormField><Button onClick={() => setValue('settings.psk', generatePsk())}>{t('generate')}</Button><Button disabled={!canCopySnellSurge(host, port, psk)} onClick={() => copy(buildSnellSurgeLine(remark, host, port, psk))}>Copy Surge v5</Button></>;
```

```ts
it('preserves empty edit PSK and emits exact Surge v5 line', () => {
  expect(formValuesToWirePayload({...old, protocol:'snell', settings:{psk:''}}).settings.psk).toBe(old.settings.psk);
  expect(buildSnellSurgeLine('edge', '198.51.100.4', 443, 'abc1234567890123')).toBe('edge = snell, 198.51.100.4, 443, psk=abc1234567890123, version=5');
});
```
- [ ] **Step 4: Run pass.** `cd frontend && npm test -- --run src/test/snell-fields.test.tsx src/test/inbound-form-adapter.test.ts && npm run typecheck`.
- [ ] **Step 5: Commit.** `git add frontend/src && git commit -m "feat: add Snell form and Surge output"`.

### Task 8: Exclude every non-Surge output and render runtime state

**Files:**
- Create: `frontend/src/test/snell-exports.test.ts`, `frontend/src/test/snell-info.test.tsx`
- Modify: `frontend/src/pages/inbounds/info/InboundInfoModal.tsx`, `info/helpers.ts`, `list/helpers.ts`, `list/RowActions.tsx`, `list/useInboundColumns.tsx`, `qr/QrCodeModal.tsx`, `frontend/src/lib/xray/inbound-link.ts`
- Modify: `internal/sub/service.go`, `internal/sub/links.go`, `internal/sub/clash_service.go`, `internal/sub/service_test.go`, `internal/sub/links_test.go`, `internal/sub/clash_service_test.go`

**Interfaces:**
- Define `function isSnell(protocol:string):boolean` and `function hasSnellSurgeCopy(protocol:string):boolean`; every subscription, `allLinks`, QR, Clash, URI, and client-management helper returns false/empty for Snell.
- `DBInbound` consumes non-persisted `runtimeStatus: InboundViewStatus`; list/detail shows `snell`, shared flow, running/error category, and no client count; only `SnellFields` copy action is exposed.

- [ ] **Step 1: Write failing tests.** Assert raw/JSON/Clash subscription and all-links services omit Snell, QR/client/URI helpers omit it, valid copy appears, invalid copy is disabled, and runtime/error state renders without client count.
- [ ] **Step 2: Run failure.** `cd frontend && npm test -- --run src/test/snell-exports.test.ts src/test/snell-info.test.tsx src/test/inbound-link.test.ts`; `go test ./internal/sub -run 'Test.*(Snell|Subscription|Export)' -count=1`.
- [ ] **Step 3: Implement explicit gates.** Add Snell checks at current protocol switch boundaries, preserve all existing protocol output, map API `runtimeStatus` without adding a column, and use the existing share-address resolver for Surge host.

```ts
export function isSnell(protocol: string): boolean { return protocol === Protocols.SNELL; }
export function showQrCodeMenu(ib: DBInboundRecord): boolean { return !isSnell(ib.protocol) && existingShowQrCodeMenu(ib); }
export function hasShareLink(protocol: string): boolean { return !isSnell(protocol) && LINK_PROTOCOLS.has(protocol); }
```

```go
func TestSubscriptionOmitsSnell(t *testing.T) {
    got, err := (&Service{}).Build([]*model.Inbound{{Protocol:model.Snell, Settings:`{"psk":"secret"}`}})
    if err != nil { t.Fatal(err) }; if strings.Contains(got, "snell") || strings.Contains(got, "secret") { t.Fatal("Snell escaped subscription") }
}
```
- [ ] **Step 4: Run pass.** Re-run both commands and `cd frontend && npm run lint`.
- [ ] **Step 5: Commit.** `git add frontend/src internal/sub && git commit -m "feat: restrict Snell sharing and exports"`.

### Task 9: Document support and complete deterministic validation

**Files:**
- Modify: `README.md`
- Test: `deploy/test/smoke-noninteractive.sh`, `frontend/src/test/snell-fields.test.tsx`, `frontend/src/test/snell-exports.test.ts`, `internal/snell/*_test.go`, `internal/web/service/inbound_snell_test.go`, `internal/web/job/snell_job_test.go`

**Interfaces:**
- README documents Linux Host, nftables kernel/permission prerequisite, rejected Docker/non-Linux/unsupported arch, fixed v5.0.1 installation, shared quota, and only Surge v5 copy.

- [ ] **Step 1: Write the README assertions.** Add exact headings and commands to `deploy/test/smoke-noninteractive.sh` that grep `README.md` for `Linux Host`, `nftables`, `Docker`, `non-Linux`, `v5.0.1`, and `Surge`.
- [ ] **Step 2: Implement README text.** State that installation/upgrade is the only download path and runtime requires a verified local binary; state `trafficReset=never` keeps a manually disabled Snell inbound disabled across monthly dates.

```bash
for phrase in 'Linux Host' nftables Docker non-Linux v5.0.1 Surge; do
  grep -Fq "$phrase" README.md || { echo "README missing: $phrase" >&2; exit 1; }
done
bash -n install.sh update.sh deploy/test/smoke-noninteractive.sh
```

```text
go test ./internal/database/model ./internal/snell ./internal/web/runtime ./internal/web/service ./internal/web/job ./internal/sub
cd frontend && npm test -- --run src/test/snell-fields.test.tsx src/test/snell-exports.test.ts src/test/inbound-form-adapter.test.ts && npm run typecheck && npm run lint
```
- [ ] **Step 3: Run focused validation.** `bash -n install.sh update.sh deploy/test/smoke-noninteractive.sh && bash deploy/test/smoke-noninteractive.sh`; `go test ./internal/database/model ./internal/snell ./internal/web/runtime ./internal/web/service ./internal/web/job ./internal/sub`; `cd frontend && npm test -- --run src/test/snell-fields.test.tsx src/test/snell-exports.test.ts src/test/inbound-form-adapter.test.ts && npm run typecheck && npm run lint`.
- [ ] **Step 4: Run final suites.** `go test ./...`; `cd frontend && npm test`.
- [ ] **Step 5: Run final self-review.** Run `rg -n '^[[:space:]]*(TEMPLATE|PLACE|UNRES)' docs/superpowers/plans/2026-08-10-snell-v5-integration.md`; require no matches. Separately inspect the plan for unresolved placeholders, map the ten specification acceptance criteria to Tasks 1–9, verify every named type/signature is defined once and consumed consistently, run `git diff --check`, and verify `git status --short` contains no plan-unrelated edits.
- [ ] **Step 6: Commit.** `git add README.md deploy/test/smoke-noninteractive.sh && git commit -m "docs: document Snell Host support"`.

## Acceptance Mapping

1. Four Host architectures, verified v5.0.1 binary, PSK inbound, one sidecar: Tasks 1–4.
2. Shared address/PSK and single inbound quota: Tasks 2, 5, 6, 7.
3. TCP/UDP `inet xui_snell` absolute counters seeded from DB: Task 3 and Task 6.
4. Hard quota stops only matching sidecar: Tasks 5–6.
5. Monthly/manual reset semantics and `trafficReset=never`: Task 6 and Task 9.
6. PSK generation, `0600`, API/log/list secrecy: Tasks 2, 3, 5.
7. Exact Surge-only output and all exclusions: Tasks 7–8.
8. Host/Docker/non-Linux/arch/binary/nft errors: Tasks 1, 3–5, 9.
9. Existing add/edit modal, SnellFields, field hiding, regenerate/copy/status: Tasks 5, 7–8.
10. Focused and existing protocol regression suites: Task 9.
