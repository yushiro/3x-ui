# Snell Server v5.0.1 Integration Design

**Status:** approved design
**Completion boundary:** source changes and local verification only; this design does not include packaging, release, or production deployment.

## Purpose and scope

Add **Snell Server v5.0.1** as a 3x-ui inbound protocol. Snell is not an
Xray-core protocol, so each enabled Snell inbound is served by its own
`snell-server` sidecar process. The implementation must support a hard,
shared monthly traffic limit for that sidecar by measuring its TCP and UDP
traffic with nftables.

One Snell inbound represents one shareable credential set, not one physical
device or one 3x-ui client:

```text
one inbound = one listen address and port + one PSK + one sidecar + one monthly quota
Mac A ─┐
Mac B ─┼── same address, port, and PSK ──> the same Snell inbound
Mac C ─┘
```

All devices using that configuration contribute to the same inbound's
`up`/`down` total. Operators that need separate allowances create separate
Snell inbounds, each with its own port, PSK, sidecar, and quota. There is no
device-level accounting in this release.

## Sources and fixed product decisions

- Snell Server is fixed to stable **v5.0.1**. v6 and every other Snell version
  are outside this release.
- The authoritative release page supplies the four v5.0.1 Linux assets and
  describes Snell as a single binary (apart from glibc):
  <https://kb.nssurge.com/surge-knowledge-base/release-notes/snell>.
- The traffic design relies on nftables named counters. `nft reset counter`
  returns the current count and resets it, which provides the required atomic
  sample-and-clear operation:
  <https://wiki.nftables.org/wiki-nftables/index.php/Counters>.

The supported server platforms are Linux hosts only:

| Environment | Result |
| --- | --- |
| Linux host with nftables and permission to manage nftables | Supported |
| Docker container | Unsupported in this release |
| Non-Linux host | Unsupported in this release |
| Linux host without `nft`, an nftables kernel, or required privileges | Snell inbound start is refused with a clear prerequisite error |

The README and all relevant UI/API error messages must state this Host-only
limitation. No Snell action may alter the availability of Xray, MTProto, or
another protocol.

## Architecture

```text
3x-ui inbound record                  Host runtime
---------------------                 ----------------------------------------
protocol: snell       ── settings ──>  0600 snell-<inbound-id>.conf
listen/port                               │
enable, up/down/total                     ▼
trafficReset/day                      snell-server v5.0.1 sidecar
                                            │ TCP and UDP on the inbound port
                                            ▼
                                      nftables table inet xui_snell
                                      counters snell_<id>_up/down
                                            │ atomic sample and reset
                                            ▼
                                      periodic Snell reconcile/traffic job
                                            │
                                            ├─ add deltas to Inbound.Up/Down
                                            └─ stop sidecar when quota is reached
```

The sidecar manager follows the existing MTProto sidecar pattern for process
ownership, reconciliation, crash recovery, and orphan cleanup. It remains a
separate Snell package/service: it must not pretend that Snell is an Xray
inbound or route Snell configuration through Xray.

### Components

1. **Inbound model and validation.** Register `snell` as a protocol while
   retaining the existing inbound fields `listen`, `port`, `enable`, `up`,
   `down`, `total`, `trafficReset`, `trafficResetDay`, and `remark`. Snell
   must participate in the existing port-conflict checks.
2. **Snell settings.** The first release stores only `psk` in the inbound
   `settings` JSON. A cryptographically secure PSK is generated for a new
   inbound; the operator may edit it or explicitly generate a replacement.
   No `clients` array and no `ClientTraffic` records are created.
3. **Binary installer.** Installation and upgrade obtain the selected official
   v5.0.1 Linux ZIP once, verify it against a fixed SHA-256 value for that
   exact asset, extract it, and install the executable under `bin/snell/`.
   The runtime never downloads an executable. The binary is not committed to
   Git. An absent hash entry, a mismatched digest, an unsupported architecture,
   or an invalid archive fails closed.
4. **Sidecar manager.** Own one process per enabled valid Snell inbound;
   render its configuration to `bin/snell/config/snell-<inbound-id>.conf`;
   and serialize mutations for each inbound so update, reconcile, quota stop,
   and delete cannot race each other.
5. **nftables manager.** Own only the dedicated `inet xui_snell` table and its
   chains, rules, and named counters. It never flushes, lists for mutation, or
   rewrites user firewall tables.
6. **Traffic/reconciliation job.** Reconciles desired sidecars, imports
   counter deltas, enforces the hard quota, and applies the Snell-specific
   monthly reset behavior described below.
7. **Inbound UI.** Adds a Snell form, its read-only traffic display, and an
   explicit Surge configuration copy action. It adds no client-management UI.

## Installation and binary management

Supported `runtime.GOARCH` values map exactly to these official v5.0.1 assets:

| Architecture | Asset |
| --- | --- |
| `amd64` | `https://dl.nssurge.com/snell/snell-server-v5.0.1-linux-amd64.zip` |
| `386` | `https://dl.nssurge.com/snell/snell-server-v5.0.1-linux-i386.zip` |
| `arm64` | `https://dl.nssurge.com/snell/snell-server-v5.0.1-linux-aarch64.zip` |
| `arm` (armv7-compatible) | `https://dl.nssurge.com/snell/snell-server-v5.0.1-linux-armv7l.zip` |

The implementation must keep an immutable SHA-256 mapping for those four URL
strings and compare the downloaded archive before extraction. No mutable
“latest” URL or fallback version is permitted. Archive extraction must reject
unexpected paths, install the executable atomically, and set executable
permissions. A failed install must leave the previously verified binary intact.

At runtime, an enabled Snell inbound first checks Linux, the installed binary,
`nft`, nftables access, and the ability to create its own rules. Missing
prerequisites prevent the enable/start transition and return a message naming
the unmet requirement; they must not cause an on-demand download.

## Configuration and client output

The rendered sidecar configuration derives its listener from the common inbound
`listen` and `port` fields and its only protocol-specific value from
`settings.psk`. It is written with mode `0600` in
`bin/snell/config/snell-<inbound-id>.conf`. Configuration syntax and the
sidecar invocation must be validated against the downloaded v5.0.1 binary.

The UI supplies exactly one client output: **Copy Surge configuration**. It
uses the existing inbound share-address resolution and emits this complete
single-line form:

```ini
<name> = snell, <host>, <port>, psk=<psk>, version=5
```

`<name>` is the inbound display name and `<host>` is the resolved shared server
address. The copy output is not a URI and is not a subscription entry.

The following must remain excluded:

- subscriptions and “export all links”;
- Clash and other non-Surge output;
- QR-code generation;
- a non-official Snell URI scheme;
- client records, per-client links, and the general Clients page.

## nftables traffic accounting

The manager creates a dedicated `inet xui_snell` table. For each inbound ID
`N`, it creates these named counters:

```text
snell_N_up
snell_N_down
```

The table has dedicated base chains that make no verdict decision, so existing
firewall policy remains in control. Rules count both TCP and UDP:

| Direction | Packet match | Counter | Inbound field |
| --- | --- | --- | --- |
| Client to Snell host | destination port is the inbound port | `snell_N_up` | `up` |
| Snell host to client | source port is the inbound port | `snell_N_down` | `down` |

The rules must be valid for both IPv4 and IPv6 through the `inet` family. They
are intended for a Snell process directly listening on the Host port; this
release does not support an extra container, NAT-only, or user-space relay
deployment as a substitute.

Each collection interval uses the named-counter reset operation to obtain and
clear an `up` and `down` byte delta atomically. The job retains successfully
read deltas in memory until it has added them to the existing inbound traffic
fields. On a temporary database failure it retries those retained deltas rather
than discarding them. It must not add a second accounting baseline column or a
traffic journal table in this release.

When the manager starts, it reconciles its own table: it creates missing
objects for desired Snell inbounds and removes only objects belonging to
nonexistent Snell inbounds. It never performs a system-wide nftables flush.
Before changing a Snell port or deleting an inbound, it stops that sidecar and
folds its final readable counter delta into the inbound's traffic before
removing the old rules. Counters are recreated for a changed port; accumulated
monthly `up` and `down` values are preserved.

## Lifecycle and quota behavior

All sidecar and nftables operations are isolated per inbound. Failure for one
Snell inbound is recorded and surfaced for that inbound only.

### Create, enable, update, disable, and delete

1. **Create or enable:** validate the host prerequisites and settings, ensure
   the verified binary and counters exist, write the `0600` configuration, and
   start one sidecar. An already-consumed nonzero `total` is enforced before
   starting, so an enabled inbound cannot remain running above its quota.
2. **Update listener, port, or PSK:** serialize the operation; stop the old
   process; collect its final traffic delta; replace configuration and affected
   counter rules; then start the replacement process if it remains enabled and
   within quota.
3. **Manual disable:** stop the process while retaining its configuration,
   counters, and accumulated monthly traffic. It does not create a special
   disable reason.
4. **Delete:** stop the process, collect final traffic where possible, delete
   its configuration and only its named counters/rules, and remove the inbound.
5. **Unexpected exit:** while the inbound is enabled and below quota, retry
   sidecar start with bounded exponential backoff. A successful intentional
   stop, a disabled inbound, and a quota stop must not schedule a restart.
6. **Startup/orphan recovery:** reconcile database state with managed
   processes; terminate orphaned Snell processes that carry the manager's
   identifiable invocation/configuration; then recreate desired enabled
   processes. It must never kill an unrelated `snell-server` process.

### Hard traffic quota

After adding each counter delta, the traffic job checks `total`. A zero or
otherwise existing “unlimited” total preserves the project's current unlimited
semantics. For a positive total, when `up + down >= total`, the job must:

1. set the inbound `enable` field to `false`;
2. stop the corresponding Snell sidecar; and
3. leave its accumulated traffic visible.

This is a hard operational limit, not only a warning. TCP and UDP stop together
because the entire sidecar stops.

### Monthly reset and automatic recovery

For a Snell inbound whose `trafficReset` is `monthly`, the existing monthly
schedule and its `trafficResetDay` determine when the reset runs. On that date,
the Snell-specific extension to the periodic reset job must:

1. stop the sidecar if it is running and collect any final readable traffic;
2. reset its nftables counters;
3. set inbound `up` and `down` to zero;
4. set inbound `enable` to `true` unconditionally; and
5. recreate/start the sidecar after prerequisites and settings validate.

There is deliberately no `disableReason`, `quotaDisabled`, or other database
field. Consequently, a manually disabled Snell inbound with a monthly reset is
also re-enabled on its next reset date. To keep a Snell inbound disabled across
monthly reset dates, an operator must set `trafficReset` to `never` before
disabling it. This automatic recovery applies only to Snell; it must not alter
the existing reset behavior of Xray, MTProto, or their clients.

## Errors, safety, and observability

- Run `nft` and `snell-server` through argument-based process execution; never
  construct a shell command from an inbound value.
- Validate listener/port, PSK, inbound ID, and counter names before using them
  in a command or a configuration file. Counter names use the numeric database
  ID only.
- Treat missing binary, unsupported platform/architecture, failed digest,
  nftables absence, insufficient privilege, invalid PSK, port collision,
  configuration write failure, and process start failure as clear per-inbound
  errors. The error text must say that Docker and non-Linux hosts are unsupported
  where that is the cause.
- Keep PSK-bearing configuration files at `0600`; do not write PSKs to logs or
  error text. List endpoints must not return a plaintext PSK. An authenticated
  inbound detail/edit response and the explicit copy action may expose it to the
  authorized operator.
- Use bounded exponential backoff for crashes, reset backoff after a stable
  running period, and log the inbound ID and safe failure category. Avoid a
  restart storm caused by a bad configuration or missing prerequisite.
- Configuration changes, quota stops, reset starts, and process-exit callbacks
  must be serialized per inbound. Collection must not revive a sidecar after a
  concurrent manual disable, delete, or quota stop.

## UI and API behavior

The Inbounds form adds `snell` to the supported protocol selector. Its form
contains the common inbound traffic/reset fields and one Snell-specific PSK
field with generate/regenerate and edit controls. It does not show transport,
TLS, Xray sniffing, or multi-client controls that do not apply to Snell.

Inbound list/detail UI identifies Snell traffic as shared inbound traffic. The
only sharing affordance is the explicit **Copy Surge configuration** action
defined above. APIs validate `settings.psk`, omit the PSK from list responses,
and preserve it when an update does not intentionally replace it. Existing
subscription APIs must continue to ignore Snell.

## Validation strategy

Tests must be deterministic and must not require a real Snell download,
privileged nftables host, or public network connection. Inject the binary
installer, process launcher, clock/scheduler, and nftables runner behind small
interfaces and use fakes in unit/service tests.

Required coverage:

1. Snell settings validation, cryptographically secure PSK generation, and
   default inbound creation.
2. Rendered configuration permissions and safe process arguments.
3. The architecture-to-official-URL table plus nonempty fixed SHA-256 mappings;
   unsupported architecture, absent mapping, mismatched digest, malformed ZIP,
   and failed replacement leave no unverified executable installed.
4. Sidecar create, enable, listener/port/PSK update, manual disable, delete,
   concurrent operation serialization, intentional stop, bounded crash restart,
   and identified-orphan cleanup using a fake `snell-server`.
5. nftables rule generation for TCP and UDP in both directions, counter-name
   validation, parse of returned counts, atomic reset request, cleanup of stale
   Snell-only objects, and no command that flushes or modifies another table.
6. Traffic delta persistence, retry after a transient database failure, quota
   exhaustion stopping only the matching process, and no impact on a second
   Snell inbound or an Xray/MTProto inbound.
7. Monthly reset clears traffic/counters and unconditionally enables and starts
   a valid Snell inbound, including one that had been manually disabled. Verify
   that no existing protocol's periodic-reset behavior changes.
8. Panel restart reconciliation, unavailable nftables, missing binary, process
   start failure, and malformed settings produce isolated errors and never
   start an unsupported Docker/non-Linux deployment.
9. Frontend schema, defaults, form validation, protocol capability exclusions,
   and exact Surge copy output. Verify Snell stays absent from client pages,
   subscriptions, QR output, Clash output, and URI/link export.
10. README coverage of Linux Host-only support, nftables/privilege prerequisites,
    Docker/non-Linux non-support, and Surge-only configuration copying.

## Acceptance criteria

The implementation is complete when all of the following are true:

1. An operator on a supported Linux Host can install a verified official Snell
   v5.0.1 binary for `amd64`, `386`, `arm64`, or `arm`, create a PSK-backed
   Snell inbound, and run exactly one sidecar for it.
2. Multiple Macs using the same copied Surge line share that inbound and its
   single `up + down` monthly quota; a second quota is represented by a second
   inbound rather than a client record.
3. TCP and UDP bytes for each direct Host listener are accounted separately as
   inbound `up` and `down` using only `inet xui_snell` named counters.
4. Reaching a positive inbound `total` disables the inbound and stops its
   sidecar without stopping another sidecar or any Xray/MTProto service.
5. A monthly reset clears Snell traffic and counters, sets `enable=true`, and
   restarts a valid Snell sidecar even when it was manually disabled before the
   reset; `trafficReset=never` is the documented way to keep it disabled.
6. PSKs are generated securely, editable, stored in `0600` runtime
   configuration, omitted from list APIs/logs, and available only through
   authorized detail/edit or explicit copy flows.
7. The only client output is the exact Surge configuration line with
   `version=5`; Snell is absent from subscriptions, QR, Clash, and URI export.
8. Docker, non-Linux, unsupported architectures, absent/unverified binary, and
   nftables privilege failures fail with clear isolated errors and are documented
   in the README.
9. The focused tests above pass, and existing affected protocol tests show no
   changed behavior outside this design.

## Explicit non-goals

- Snell v6, beta releases, alternate server versions, or a mutable “latest”
  channel.
- Docker, Kubernetes, macOS, Windows, or non-Linux server support.
- A container relay, user-space TCP/UDP proxy, or an alternative to nftables
  accounting.
- Per-device, per-user, or per-client traffic accounting; `ClientTraffic`; or
  a synthetic Snell client model.
- Xray-core support, Xray transports/security settings, Xray routing, or
  changing Xray behavior.
- Subscription integration, share links/URIs, QR codes, Clash conversion, or
  any non-Surge client output.
- Snell advanced options such as egress-interface, custom DNS, ShadowTLS, or
  obfuscation.
- New `disableReason`, `quotaDisabled`, or similar persistent state, database
  migration, or a generalized change to existing protocol reset semantics.
- Changing user-managed nftables tables, chains, rules, policies, or unrelated
  `snell-server` processes.

## Risks and contained mitigations

| Risk | Contained mitigation |
| --- | --- |
| Host lacks nftables privileges | Refuse that inbound start with a prerequisite error; do not provide a lower-fidelity fallback. |
| Sidecar crash/restart loop | Bound retries with exponential backoff and stop scheduling when disabled or quota-stopped. |
| Misconfigured update loses a live listener | Serialize per-inbound lifecycle operations, collect final traffic before rule replacement, and start the replacement only after validation. |
| User firewall interference | Use only `inet xui_snell`, pass no verdict, and never flush or mutate another table. |
| PSK disclosure | Use `0600` files, redact logs/list responses, and restrict plaintext to authorized edit/copy flows. |
| Accounting write is temporarily unavailable | Retain sampled deltas in memory and retry instead of dropping them; no database schema is added in this scoped release. |
| Shared credentials cannot identify Macs | Treat the inbound as a deliberately shared allowance; require separate inbounds for separate allowances. |
