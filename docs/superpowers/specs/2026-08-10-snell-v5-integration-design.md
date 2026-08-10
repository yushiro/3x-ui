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
- The traffic design relies on nftables named counters. The nftables
  documentation covers declaring a named counter with initial `packets` and
  `bytes` values and listing its current values, which lets the database and
  counter represent the same monthly absolute totals:
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
                                            │ read absolute byte values
                                            ▼
                                      periodic Snell reconcile/traffic job
                                            │
                                            ├─ sync values to Inbound.Up/Down
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
6. **Traffic/reconciliation job.** Reconciles desired sidecars, synchronizes
   absolute counter totals, enforces the hard quota, and applies the Snell-specific
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

Each named counter is the absolute byte total for the inbound's current traffic
reset period. A regular collection interval only lists `snell_N_up` and
`snell_N_down`, then writes their returned byte values directly to
`Inbound.Up` and `Inbound.Down`; it never resets a counter, calculates a
separate per-poll value, or keeps accounting state in memory. If the database
write fails, the counter remains unchanged and the next collection naturally
retries the same absolute values. No accounting baseline column or traffic
journal table is added in this release.

New counters start from the inbound's current database values, for example
`snell_N_up` starts with `packets 0 bytes Inbound.Up` and `snell_N_down` starts
with `packets 0 bytes Inbound.Down`. This seed rule applies whenever an object
must be created or rebuilt. Thus an enabled inbound that changes port, has its
managed rules restored, or is reconciled after a panel/Host restart retains the
monthly total rather than double-counting or rewinding it.

When the manager starts, it reconciles its own table: it lists existing managed
counters first, retains valid counters whose values are at least the database
value, creates only missing objects with the database seed, and removes only
objects belonging to nonexistent Snell inbounds. It never performs a
system-wide nftables flush. If a counter is missing while its sidecar could be
serving traffic, the manager first stops that sidecar, creates the seeded
counter/rule, and then restores the sidecar so no live interval is silently
unmetered.

A counter value below the database `up` or `down` value is an accounting
anomaly, except during the serialized reset operation described below. The
manager must stop that inbound's sidecar, rebuild only the lower counter/rule
with the database value as its initial bytes, and then resume the sidecar only
when the inbound remains enabled and within quota. It must never overwrite the
larger database value with the lower counter value. Before a port change or
delete, the manager stops the sidecar and synchronizes the final absolute
counter values. A port change then recreates counters seeded with those values;
delete removes only that inbound's rules and counters.

Counters and database traffic are reset together only by the monthly reset or
an explicit manual inbound traffic-reset operation. Both operations serialize
with collection and lifecycle changes and stop the affected sidecar. The reset
first sets the two named counters to zero and then writes both database values
as zero. If the database write fails, the reset reports failure; the normal
lower-counter recovery restores the counters from the still-nonzero database
values, so the reset does not silently create a lower total. A successful reset
therefore leaves both stores at zero before applying its restart behavior. A
manual reset preserves the inbound's enable state; the monthly reset follows
the automatic recovery rules below.

## Lifecycle and quota behavior

All sidecar and nftables operations are isolated per inbound. Failure for one
Snell inbound is recorded and surfaced for that inbound only.

### Create, enable, update, disable, and delete

1. **Create or enable:** validate the host prerequisites and settings, ensure
   the verified binary and counters exist, write the `0600` configuration, and
   start one sidecar. An already-consumed nonzero `total` is enforced before
   starting, so an enabled inbound cannot remain running above its quota.
2. **Update listener, port, or PSK:** serialize the operation; stop the old
   process; synchronize its final absolute counter values; replace configuration
   and affected counter rules seeded from those values; then start the
   replacement process if it remains enabled and within quota.
3. **Manual disable:** stop the process while retaining its configuration,
   counters, and accumulated monthly traffic. It does not create a special
   disable reason.
4. **Delete:** stop the process, synchronize final absolute traffic where
   possible, delete its configuration and only its named counters/rules, and
   remove the inbound.
5. **Unexpected exit:** while the inbound is enabled and below quota, retry
   sidecar start with bounded exponential backoff. A successful intentional
   stop, a disabled inbound, and a quota stop must not schedule a restart.
6. **Startup/orphan recovery:** reconcile database state with managed
   processes; terminate orphaned Snell processes that carry the manager's
   identifiable invocation/configuration; then recreate desired enabled
   processes. It must never kill an unrelated `snell-server` process.

### Hard traffic quota

After synchronizing each counter's absolute values, the traffic job checks
`total`. A zero or
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

1. stop the sidecar if it is running and enter the serialized reset operation;
2. reset its nftables counters and inbound `up`/`down` values to zero as
   described in the accounting section;
3. set inbound `enable` to `true` unconditionally; and
4. recreate/start the sidecar after prerequisites and settings validate.

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
   validation, initial-byte seeding, and parsing/listing of absolute counts;
   cleanup of stale Snell-only objects; and no command that flushes or modifies
   another table.
6. Absolute-value synchronization writes named-counter bytes to the inbound;
   a transient database failure is retried by the next unchanged counter read;
   counter creation/recreation seeds from the database; and a counter lower
   than the database stops, rebuilds, and only then resumes that sidecar without
   an accounting rollback. Cover port update, missing-rule recovery, and panel
   restart so none duplicate or omit already recorded monthly traffic.
7. Quota exhaustion stops only the matching process and has no impact on a
   second Snell inbound or an Xray/MTProto inbound. Monthly reset and explicit
   manual reset clear both database values and named counters; monthly reset
   unconditionally enables and starts a valid Snell inbound, including one that
   had been manually disabled, while manual reset preserves enable state. Verify
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
   absolute inbound `up` and `down` values using only `inet xui_snell` named
   counters. Regular collection never resets a counter; counter creation,
   recovery, and port replacement seed it with the existing database total.
4. Reaching a positive inbound `total` disables the inbound and stops its
   sidecar without stopping another sidecar or any Xray/MTProto service.
5. A monthly reset clears Snell traffic and counters, sets `enable=true`, and
   restarts a valid Snell sidecar even when it was manually disabled before the
   reset; an explicit manual reset clears both stores while preserving enable
   state; and `trafficReset=never` is the documented way to keep it disabled.
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
| Misconfigured update loses a live listener | Serialize per-inbound lifecycle operations, synchronize final absolute totals before rule replacement, seed the replacement counters from those totals, and start only after validation. |
| User firewall interference | Use only `inet xui_snell`, pass no verdict, and never flush or mutate another table. |
| PSK disclosure | Use `0600` files, redact logs/list responses, and restrict plaintext to authorized edit/copy flows. |
| Accounting write is temporarily unavailable | Leave the named counter unchanged; the next collection writes the same absolute value, with no per-poll in-memory accounting state or database schema required. |
| Counter value falls below the database total | Stop only that sidecar, rebuild its own counter/rule seeded from the larger database value, then resume only if enabled and under quota. |
| Shared credentials cannot identify Macs | Treat the inbound as a deliberately shared allowance; require separate inbounds for separate allowances. |
