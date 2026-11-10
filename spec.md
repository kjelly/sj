# `sj` — systemd-native User Job Manager
## Implementation Specification for Coding Agent

**Status:** Implementation-ready specification
**Target platform:** Linux with `systemd --user`
**Primary language:** Go
**CLI working name:** `sj`
**Design inspiration:** Pueue CLI UX, systemd transient units, existing Nushell workflow
**Architecture rule:** `sj` itself is a short-lived/stateless CLI. **Do not add an `sj` daemon.** systemd is the process supervisor and authoritative runtime state.

---

# 1. Objective

Implement a lightweight, systemd-native CLI for starting, inspecting, controlling, and following background jobs owned by the current user.

The tool replaces an existing Nushell workflow that currently uses:

- `systemd-run --user`
- `systemctl --user`
- `journalctl --user`
- a fixed set of unit slots such as `run.service`, `run1.service`, ...
- optional `direnv`
- optional `local.nu`
- delayed notes/timers
- log inspection/following

The new implementation MUST improve correctness and maintainability without becoming a general-purpose scheduler.

The desired product can be summarized as:

> **Pueue-style CLI ergonomics + systemd-native execution + no scheduler daemon.**

---

# 2. Core design decisions

The following decisions are already made and MUST NOT be revisited unless implementation proves them impossible.

## 2.1 No daemon

`sj` MUST be a short-lived CLI process.

Correct architecture:

```text
sj CLI
  |
  +-- systemd user D-Bus --> systemd --user --> jobs/services
  |
  +-- journalctl ---------> journald
  |
  `-- systemd-run --------> interactive PTY mode only
```

Incorrect architecture:

```text
sj CLI
  |
  v
sj daemon
  |
  v
systemd
```

Do not implement:

- Unix socket server
- background scheduler
- persistent worker
- daemon-maintained state
- daemon-owned logging

systemd is already the supervisor and the primary source of runtime truth.

---

## 2.2 Use Pueue as CLI inspiration, not as an architecture template

Adopt the good user-facing concepts from Pueue:

- `status`
- `log`
- `follow`
- `wait`
- `restart`
- `pause`
- `resume`
- `kill`
- `clean`
- machine-readable JSON
- task labels/descriptions
- simple numeric job identifiers
- optional immediate follow after starting

Do **not** implement Pueue scheduler concepts in v1:

- queued tasks waiting for worker slots
- `stash`
- `enqueue`
- task priority
- task dependencies
- worker groups
- group parallelism
- persistent task database
- queue reordering
- daemon-owned task state

If all configured job slots are occupied, `sj run` MUST return a clear `no free job slots` error. It MUST NOT silently build an internal queue.

---

# 3. Scope

## 3.1 In scope

### Finite jobs

Examples:

```bash
sj run 'go test ./...'
sj run 'git pull; make'
sj exec -- rsync -av "foo bar" /tmp/
```

Finite jobs use a bounded slot pool:

```text
sj-run-1.service
sj-run-1.service
sj-run-2.service
...
```

The slot count is configurable.

### Named long-running transient services

Examples:

```bash
sj service run ollama -- ollama serve
sj service run dev-web -- ./server --port 8080
```

Named services are NOT part of the finite-job slot pool.

They use:

```text
sj-svc-ollama.service
sj-svc-dev\x2dweb.service
```

or another deterministic systemd-safe escaped representation.

### Delayed jobs

Examples:

```bash
sj run --delay 30m 'backup-script'
sj run --at '2026-09-08 02:00' 'backup-script'
```

Use transient `.timer` + `.service` units.

### Notes/reminders represented as units

Examples:

```bash
sj note 'server maintenance'
sj note --after 30m 'check backup'
sj note --ttl 2h 'temporary reminder'
```

### Job control

- status
- logs
- streaming logs
- wait
- stop
- kill/signal
- pause/freeze
- resume/thaw
- restart where enough unit metadata still exists
- cleanup/reset

### Shell integration

- Nushell command execution
- `local.nu`
- `direnv`
- shell completion
- prompt/status fragment
- stable JSON output for scripting

---

## 3.2 Explicitly out of scope for v1

Do not implement:

- multi-host execution
- SSH orchestration
- distributed scheduling
- persistent queue
- SQLite task history
- permanent process history database
- task dependency DAG
- retries/backoff scheduler
- priority queues
- GPU-aware scheduling
- load-aware scheduling
- web UI
- REST/gRPC API
- desktop daemon
- automatic installation of permanent `.service` files
- management of arbitrary root/system units
- root privilege escalation
- Windows/macOS support

---

# 4. Terminology

## Job

A finite task launched in a numbered slot.

Example:

```text
job ID: 3
unit: sj-run-3.service
```

A numeric job ID is a **slot identifier**, not a globally permanent task ID.

After the old invocation disappears, slot `3` may be reused.

## Invocation

One concrete execution of a systemd service.

Its unique identity is systemd's `InvocationID`.

Use `InvocationID` whenever log isolation is required.

## Service

A named long-running transient service managed by `sj`.

Example:

```text
name: ollama
unit: sj-svc-ollama.service
```

## Scheduled job

A finite job whose slot is reserved by a transient timer/service pair.

Example:

```text
sj-run-2.timer
sj-run-2.service
```

---

# 5. Source of truth

## 5.1 Runtime source of truth

systemd MUST be the authoritative source for:

- whether a unit exists
- unit active state
- unit sub-state
- failed state
- PID
- invocation ID
- start/end result
- timer state
- service properties

Do not keep a second authoritative runtime state file.

## 5.2 Journald source of truth

journald MUST be the authoritative source for process output.

Do not implement custom stdout/stderr log files.

## 5.3 Optional local metadata

v1 SHOULD avoid persistent metadata entirely.

A tiny compatibility/config cache is acceptable only if required for non-authoritative information, but:

- it MUST NOT be required to determine whether a process is currently running
- it MUST NOT become a scheduler state store
- it MUST NOT be needed for recovery after CLI crashes

---

# 6. Unit naming

Use a dedicated namespace to avoid collisions with unrelated systemd units.

## 6.1 Jobs

```text
sj-run-1.service
sj-run-1.service
...
sj-run-N.service
```

Scheduled jobs:

```text
sj-run-1.timer
sj-run-1.service
```

## 6.2 Named services

```text
sj-svc-<escaped-name>.service
```

Names MUST be escaped using systemd-compatible escaping rules.

Prefer a real systemd escaping implementation/library helper. Do not invent an ambiguous ad-hoc escaping scheme.

## 6.3 Notes

Use the finite slot namespace so notes intentionally consume job slots:

```text
sj-run-3.service
sj-run-3.timer
```

This preserves the current workflow where notes appear together with running background jobs.

---

# 7. Job slot allocation

## 7.1 Requirement

Slot allocation MUST be race-safe across multiple concurrently running `sj` CLI processes.

The old pattern is forbidden:

```text
find free slot
then
create unit
```

That is a TOCTOU race.

## 7.2 Required algorithm for immediate jobs

For each slot in ascending order:

```text
1 .. max_jobs
```

attempt to atomically create/start:

```text
sj-run-<slot>.service
```

using systemd's `StartTransientUnit` with mode equivalent to `fail`.

Pseudo-code:

```go
for slot := 1; slot <= cfg.MaxJobs; slot++ {
    name := jobServiceUnit(slot)

    err := systemd.StartTransientUnit(name, "fail", props, nil)

    if errors.Is(err, ErrUnitExists) {
        continue
    }

    if err != nil {
        return StartError{Slot: slot, Err: err}
    }

    return slot, nil
}

return ErrNoFreeSlots
```

Only a unit-name collision may advance to the next slot.

Permission errors, invalid properties, missing executables, D-Bus errors, etc. MUST immediately fail instead of trying more slots.

## 7.3 Scheduled jobs

A scheduled job MUST reserve its slot for the entire waiting period.

The implementation SHOULD atomically create:

```text
sj-run-<slot>.timer
sj-run-<slot>.service
```

in one `StartTransientUnit` transaction using auxiliary transient units if supported.

The timer is the activation unit and MUST point to the corresponding service.

If atomic timer + service creation is not practical with the selected Go library, implement a carefully documented D-Bus fallback that cannot accidentally start a different command in a reused slot.

Do not implement a userspace scheduler to solve this.

---

# 8. State model

Do not infer "running" from `ExecStart != null`.

Normalize systemd properties into these CLI-facing states:

```text
free
scheduled
starting
running
paused
success
failed
stopping
unknown
```

Recommended mapping:

| Condition | CLI state |
|---|---|
| neither service nor timer loaded | `free` |
| timer active/waiting | `scheduled` |
| service `ActiveState=activating` | `starting` |
| service `ActiveState=active` | `running` |
| service frozen | `paused` |
| service completed successfully but still inspectable | `success` |
| `ActiveState=failed` | `failed` |
| `ActiveState=deactivating` | `stopping` |
| anything unexpected | `unknown` |

Do not hide unknown states.

JSON output MUST preserve underlying systemd values too:

```json
{
  "state": "running",
  "active_state": "active",
  "sub_state": "running"
}
```

---

# 9. Finite job execution semantics

## 9.1 `sj run`

`run` is shell-oriented and preserves the existing Nushell workflow.

Canonical usage:

```bash
sj run 'git pull; make'
sj run --label 'Build hufu' 'go build ./...'
```

Simple command arguments MAY also be accepted:

```bash
sj run go test ./...
```

but shell-sensitive commands SHOULD be passed as one quoted script string.

`run` MUST execute through the configured shell.

Default shell:

```text
nu
```

Default conceptual command:

```text
nu -c '<script>'
```

If `local.nu` exists in the effective working directory and local config is enabled:

```text
source local.nu;
<script>
```

If `direnv` exists and direnv integration is enabled:

```text
direnv exec <working-dir> nu -c '<script>'
```

Do not manually import `direnv export` into the Go environment unless there is a proven reason.

### Security/quoting

The shell command MUST be passed as one argv element to `nu -c`.

Never construct:

```text
sh -c "..."
```

using unescaped string interpolation into another shell layer.

Construct the exact argv array.

---

## 9.2 `sj exec`

`exec` is exact-argv execution and MUST NOT invoke a shell.

Example:

```bash
sj exec -- rsync -av "foo bar" /tmp/
```

Required argv semantics:

```text
argv[0] = rsync
argv[1] = -av
argv[2] = foo bar
argv[3] = /tmp/
```

No joining and re-splitting.

`local.nu` does not apply to `exec`.

`direnv` MAY still wrap exact execution:

```text
direnv exec <working-dir> rsync -av "foo bar" /tmp/
```

That wrapper MUST also preserve argv boundaries.

---

# 10. Working directory

Both `run` and `exec` support:

```text
-w, --working-directory PATH
```

Default:

```text
current working directory of the `sj` process
```

Resolve to an absolute path before submitting to systemd.

If the path does not exist or is not a directory, fail before D-Bus submission.

The systemd transient unit SHOULD set `WorkingDirectory=`.

`direnv exec` MUST receive the same working directory.

---

# 11. Label / description

Use:

```text
-l, --label TEXT
```

Compatibility alias:

```text
--doc TEXT
```

If omitted:

- `run`: derive label from the command/script, truncated to a reasonable display width
- `exec`: derive label by shell-safe-ish display rendering of argv

systemd:

```text
Description=<label>
```

For notes:

```text
Description=📓<text>
```

The label is display metadata only. It MUST NOT alter task logic.

---

# 12. Service type

## 12.1 Finite jobs

Use a service type appropriate for a process expected to terminate.

The implementation MAY use `Type=exec` or `Type=oneshot` depending on what yields the cleanest state semantics, but MUST satisfy:

- start errors should be observable
- execution result must be inspectable
- successful and failed jobs must remain inspectable until explicitly cleaned/reset
- `-c`/`--collect` should make a successful job automatically free its slot

Do not automatically collect successful `run` or `exec` jobs unless the user
passes `-c`/`--collect`.

## 12.2 Long-running named services

Default:

```ini
Type=exec
Restart=on-failure
RestartSec=2s
```

Expose:

```text
--restart <no|on-success|on-failure|on-abnormal|on-watchdog|on-abort|always>
--restart-sec <duration>
```

Default restart policy for `sj service run`:

```text
on-failure
```

Allow:

```text
--type exec
--type notify
```

For notify services:

```ini
Type=notify
NotifyAccess=main
```

Do not auto-detect notify support.

---

# 13. Collection/lifecycle policy

Desired finite-job behavior:

```text
success
  -> unit remains inspectable
  -> slot remains occupied
  -> clean/reset frees slot

success with -c/--collect
  -> unit becomes collectible
  -> slot becomes reusable

failure
  -> failed unit remains inspectable
  -> slot remains occupied
  -> user can inspect logs
  -> clean/reset-failed frees slot
```

Do not enable aggressive failed-unit garbage collection for normal jobs.

Notes MAY use more aggressive collection after their TTL expires.

---

# 14. CLI command surface

The canonical top-level interface:

```text
sj run
sj exec
sj status
sj log
sj follow
sj wait
sj stop
sj kill
sj pause
sj resume
sj restart
sj clean
sj reset
sj note
sj service
sj prompt
sj doctor
sj completions
```

Aliases MAY be added, but canonical commands MUST remain stable.

---

# 15. `sj run`

Syntax:

```text
sj run [OPTIONS] <SCRIPT...>
```

Options:

```text
-l, --label TEXT
    --doc TEXT                 alias of --label
-w, --working-directory PATH
-d, --delay DURATION
    --at DATETIME
-f, --follow
-q, --quiet
-c, --collect
    --print-id
    --no-direnv
    --no-local
    --shell SHELL
```

Rules:

- `--delay` and `--at` conflict
- `--follow` is invalid for a job that has not started yet unless behavior is explicitly implemented as "wait then follow"
- `--quiet` / `--print-id` MUST provide script-friendly output
- `--collect` opts successful jobs into automatic collection; without it,
  completed jobs remain loaded until `clean` or `reset`
- if no slot is available, exit nonzero

Recommended successful normal output:

```text
Started job 3: Build hufu
```

`--print-id` output MUST contain only:

```text
3
```

plus newline.

No decoration on stdout.

Diagnostics go to stderr.

---

# 16. `sj exec`

Syntax:

```text
sj exec [OPTIONS] -- COMMAND [ARG...]
```

Use mostly the same options as `run` except:

- no `--shell`
- no `--no-local` requirement because local.nu is never sourced

Example:

```bash
sj exec -l backup -- rsync -a "/src/a b/" /backup/
```

---

# 17. `sj status`

Syntax:

```text
sj status [ID...]
sj status --all
sj status --running
sj status --failed
sj status --json
```

Default output includes all occupied finite-job slots and optionally managed named services in a separate section.

Recommended human table:

```text
ID  STATE      LABEL                     AGE      UNIT
1   running    Build hufu                1m32s    sj-run-1.service
2   scheduled  Backup                    in 28m   sj-run-2.timer
3   failed     Unit tests                5m14s    sj-run-3.service
```

Do not show free slots by default.

Optional:

```text
--free
```

may include them.

### JSON schema

`sj status --json` MUST emit stable JSON.

Top-level object:

```json
{
  "schema_version": 1,
  "jobs": [],
  "services": []
}
```

Job object minimum schema:

```json
{
  "id": 2,
  "unit": "sj-run-2.service",
  "timer_unit": null,
  "kind": "job",
  "state": "failed",
  "active_state": "failed",
  "sub_state": "failed",
  "description": "Unit tests",
  "working_directory": "/home/user/src/hufu",
  "invocation_id": "hex-or-empty",
  "main_pid": 0,
  "result": "exit-code",
  "exec_main_status": 1,
  "started_at": "2026-09-07T15:20:12+08:00",
  "finished_at": "2026-09-07T15:21:04+08:00"
}
```

Unknown/unavailable fields SHOULD be `null`, `0`, or empty according to schema, not omitted unpredictably.

Do not expose environment variables by default.

---

# 18. `sj log`

Syntax:

```text
sj log [ID...]
sj log ID --lines 50
sj log ID --full
sj log --failed
sj log --json
```

Default behavior:

- one selected job: show a useful recent amount of log
- multiple jobs: show a compact limited number of lines per job
- default line count comes from config

## 18.1 Invocation isolation

Do NOT locate logs by scanning for text such as:

```text
systemd ... Starting ...
```

Use `InvocationID`.

Preferred strategies:

1. Obtain `InvocationID` from unit properties and query journald by `_SYSTEMD_INVOCATION_ID`.
2. On sufficiently new systemd/journalctl, `journalctl --invocation` may be used where reliable.
3. If the unit invocation is gone and no InvocationID is available, fall back to unit-level logs and clearly mark the limitation.

Never parse locale-dependent "Starting" lines as an invocation boundary.

## 18.2 journalctl subprocess

Using `journalctl` as a subprocess is an intentional design decision.

Do not add CGO merely to call libsystemd journal APIs.

Pass argv directly; do not invoke through a shell.

---

# 19. `sj follow`

Syntax:

```text
sj follow [ID]
sj follow ID --lines 20
```

If ID is omitted:

- if exactly one finite job is actively running, select it
- otherwise fail with a message listing candidate IDs

Implementation:

```text
journalctl --user ... -f
```

with the current invocation filter whenever possible.

`follow` MUST forward SIGINT/SIGTERM appropriately to the local `journalctl` process without stopping the managed job.

Exiting follow MUST NOT stop the job.

---

# 20. `sj wait`

Syntax:

```text
sj wait ID...
sj wait --all
sj wait ID --quiet
```

Wait for selected jobs to reach a terminal state.

Terminal states:

```text
success
failed
free-after-observed-success
```

For multiple jobs:

- return 0 only if every observed job succeeds
- return nonzero if any fails or disappears unexpectedly

Recommended exit behavior:

```text
0   all successful
1   one or more jobs failed
2+  CLI/infrastructure/usage error
```

Implementation SHOULD use D-Bus signals/events where straightforward.

Polling at a modest interval is acceptable if simpler and reliable.

Do not busy-loop.

---

# 21. `sj stop`

Syntax:

```text
sj stop ID...
sj stop --all
```

Meaning:

> graceful systemd lifecycle stop

For running service:

```text
StopUnit()
```

For scheduled job:

- stop timer
- stop service if it has already started
- reset failed state as appropriate

For failed job:

- reset failed state to release slot

`stop` is different from `kill`.

Idempotency:

- stopping an already-free slot SHOULD be a nonfatal informative result unless strict mode is requested
- multiple IDs should all be attempted and failures summarized

---

# 22. `sj kill`

Syntax:

```text
sj kill ID...
sj kill ID --signal SIGINT
sj kill ID --signal SIGKILL
```

Meaning:

> send a signal to the unit/process cgroup

Default signal MAY be `SIGTERM`, but the CLI MUST make it clear that `stop` is the preferred graceful operation.

Use systemd D-Bus `KillUnit` / equivalent.

Signal parser MUST accept conventional forms such as:

```text
INT
SIGINT
2
```

Do not use shell `kill` command.

---

# 23. `sj pause` and `sj resume`

Syntax:

```text
sj pause ID...
sj resume ID...
```

Preferred implementation:

```text
org.freedesktop.systemd1.Manager.FreezeUnit
org.freedesktop.systemd1.Manager.ThawUnit
```

or equivalent unit-level D-Bus methods.

Requirements:

- support requires cgroup v2/unified hierarchy
- `sj doctor` MUST report whether freeze/thaw is supported
- if unsupported, return a clear capability error
- do not silently downgrade to fragile PID-only SIGSTOP/SIGCONT unless explicitly implemented and documented

State output SHOULD display:

```text
paused
```

when the unit is known to be frozen.

---

# 24. `sj restart`

Syntax:

```text
sj restart ID...
sj restart ID --follow
```

v1 semantics:

- restart a currently loaded running/failed/scheduled job using inspectable unit metadata
- preserve command, working directory, label and relevant execution options where they can be reliably recovered
- keep the same slot ID

If a completed job has been cleaned, or a successful `--collect` job has been
garbage-collected, and its command metadata is no longer available:

```text
error: job 3 is no longer loaded; sj does not maintain persistent task history
```

This limitation is intentional.

Do NOT create a task database solely to emulate Pueue's restart history.

---

# 25. `sj clean`

Pueue-inspired safer semantics:

```text
sj clean
```

MUST NOT kill healthy running jobs by default.

It should:

- reset failed finite-job units
- remove/stop expired/stale timers
- clean inspectable inactive leftovers
- leave active running jobs untouched
- leave active scheduled jobs untouched unless stale/broken

Options:

```text
--failed-only
--dry-run
```

Output example:

```text
Cleaned 2 failed jobs and 1 stale timer.
```

---

# 26. `sj reset`

Destructive cleanup equivalent to the old Nushell `clean`.

Syntax:

```text
sj reset
sj reset --force
```

Behavior:

- stop every `sj-run-*` timer
- stop every `sj-run-*` service
- stop every `sj-svc-*` service only if `--services` is supplied
- reset failed states
- ensure finite job slots become free

Interactive TTY invocation SHOULD ask for confirmation unless `--force`.

Non-interactive invocation without `--force` SHOULD fail rather than hang waiting for input.

This command replaces the old "run clean twice" workaround.

The implementation MUST converge in one invocation.

---

# 27. `sj note`

Syntax:

```text
sj note [OPTIONS] TEXT
```

Options:

```text
-a, --after DURATION
    --at DATETIME
-t, --ttl DURATION          default: infinity
-l, --label TEXT            normally generated from TEXT
```

Behavior:

### Immediate note

Reserve a finite job slot and create a lightweight active unit whose description is:

```text
📓<text>
```

The unit remains until:

- TTL expires
- user runs `sj stop ID`
- user runs `sj reset`

### Delayed note

Create timer + service pair.

While timer is waiting, status:

```text
scheduled
```

After activation, note remains active for TTL.

Avoid using a busy process.

Using `/usr/bin/sleep` is acceptable.

---

# 28. `sj service`

Named long-running transient services.

Command surface:

```text
sj service run NAME [OPTIONS] -- COMMAND [ARG...]
sj service status [NAME...]
sj service log NAME
sj service follow NAME
sj service stop NAME...
sj service restart NAME...
sj service kill NAME... [--signal SIGNAL]
sj service pause NAME...
sj service resume NAME...
sj service list
```

## 28.1 Service run

Example:

```bash
sj service run ollama \
  --restart on-failure \
  --restart-sec 2s \
  -- ollama serve
```

Properties:

```text
Type=exec
Restart=on-failure
RestartSec=2s
WorkingDirectory=<cwd>
Description=<label or service name>
```

Named services do not consume finite-job slots.

Name collisions MUST fail unless an explicit replacement/restart option is supplied.

Do not silently replace an existing service.

## 28.2 Reboot persistence

Transient `sj service run` services are intentionally ephemeral across reboot/systemd manager recreation.

For truly permanent services, documentation SHOULD recommend creating a normal user unit:

```text
~/.config/systemd/user/foo.service
```

Do not implement persistent service-file generation in v1.

---

# 29. `sj prompt`

Replacement for existing `bg-running`.

Syntax:

```text
sj prompt
sj prompt --json
sj prompt --tmux
```

Default compact example:

```text
[1:🟢Build] [2:⏰Backup] [3:❌Tests]
```

Suggested icon mapping:

```text
starting/running  🟢
scheduled         ⏰
failed            ❌
paused            ⏸
```

Requirements:

- fast
- no pager
- no unnecessary journal queries
- failure should produce empty output by default so shell prompts are not broken
- `--strict` may expose errors

Prompt rendering MUST use only systemd properties and descriptions, not logs.

`--tmux` emits a single-line status fragment with slot and state counts plus
available per-job details: ID, normalized state, label, current elapsed time,
PID, and working-directory basename. Success and failure MUST use compact emoji
markers (`✅` and `❌`) without rendering completion age or exit-code/result
text. Scheduled jobs MUST omit countdown and trigger-time details.
User-controlled text MUST be stripped of control characters and escaped so it
cannot inject tmux formats or styles.

When the detailed rendering exceeds 45 terminal columns, `--tmux`
MUST fall back to one token per job containing only its slot and status marker.

---

# 30. `sj doctor`

Syntax:

```text
sj doctor
```

Checks:

```text
systemd user manager       OK/FAIL
D-Bus user connection      OK/FAIL
systemd version            value
cgroup version             v1/v2
freeze/thaw support        YES/NO
journalctl                 path/version
nu                         path/missing
direnv                     path/missing
linger                     enabled/disabled/unknown
working config             path
max job slots              N
```

Do not automatically modify the system.

Optional explicit fix:

```text
sj doctor --fix-linger
```

may run:

```text
loginctl enable-linger <current-user>
```

only after an explicit flag.

Never enable linger silently.

---

# 31. Interactive PTY command (`sr` compatibility)

Provide either:

```text
sj shell-run -- COMMAND...
```

or compatibility alias:

```text
sj sr -- COMMAND...
```

The implementation SHOULD delegate PTY plumbing to:

```text
systemd-run --user --pty --pipe --collect ...
```

Do not reimplement PTY/session forwarding in Go in v1.

This is the primary intentional use of `systemd-run` as a subprocess.

---

# 32. Configuration

Config path:

```text
$XDG_CONFIG_HOME/sj/config.toml
```

fallback:

```text
~/.config/sj/config.toml
```

Suggested schema:

```toml
max_jobs = 8
unit_prefix = "sj"

shell = "nu"
source_local_nu = true
local_nu_filename = "local.nu"

direnv = "auto"  # auto | always | never

default_log_lines = 50
multi_log_lines = 5

service_restart = "on-failure"
service_restart_sec = "2s"

color = "auto"   # auto | always | never
```

Environment variable overrides SHOULD use:

```text
SJ_MAX_JOBS
SJ_SHELL
SJ_DIRENV
SJ_COLOR
SJ_CONFIG
```

Precedence:

```text
CLI flag
> environment
> config file
> built-in default
```

Config parse errors MUST fail clearly.

Do not silently ignore malformed config.

---

# 33. CLI framework

Preferred:

```text
github.com/alecthomas/kong
```

Reason:

- command tree is moderate
- struct-based declaration
- low ceremony
- good fit for a single binary

Cobra is acceptable only if there is a concrete feature requirement that Kong cannot satisfy cleanly.

Do not introduce a framework abstraction layer unnecessarily.

---

# 34. systemd Go library

Preferred:

```text
github.com/coreos/go-systemd/v22/dbus
```

Use the user manager connection.

Required operations include:

- StartTransientUnit
- StopUnit
- RestartUnit where appropriate
- ResetFailedUnit
- KillUnit
- unit property inspection
- list/query units
- FreezeUnit / ThawUnit via raw D-Bus if wrapper is unavailable

If go-systemd lacks one modern Manager method, use the underlying D-Bus connection for that specific method instead of falling back to stdout parsing.

---

# 35. D-Bus before command-line parsing

For management operations, preferred order is:

```text
D-Bus
> well-defined systemctl fallback
> textual parsing as last resort
```

Do not implement the core by repeatedly executing:

```text
systemctl --user show ...
```

and parsing `KEY=VALUE` unless a specific property/API gap requires it.

If fallback parsing is needed:

- request exact properties with `--property=`
- use machine-friendly values
- isolate fallback code
- add tests

---

# 36. `journalctl` policy

Using `journalctl` is preferred over binding libsystemd journal APIs.

Reasons:

- avoids CGO
- journalctl already solves follow/pager/output concerns
- reduces implementation complexity

Always invoke it with argv, not shell.

Use:

```text
--user
--no-hostname
--no-pager
--output=cat
```

as appropriate.

For follow:

```text
-f
```

For invocation filtering, use `InvocationID`.

Do not rely on human-readable systemd message text.

---

# 37. Output streams

Rules:

## stdout

Contains intended command output:

- tables
- JSON
- numeric ID for `--print-id`
- prompt fragment
- logs

## stderr

Contains:

- diagnostics
- warnings
- errors
- fallback notices
- capability failures

This guarantees:

```bash
id=$(sj run --print-id 'go test ./...')
```

works without filtering text.

---

# 38. Color

Support:

```text
--color auto
--color always
--color never
```

Default:

```text
auto
```

JSON output MUST never contain ANSI escape sequences.

`NO_COLOR` SHOULD be honored.

---

# 39. Exit codes

Reserve a small stable set.

Recommended:

```text
0  success
1  managed job/process failed or requested state operation did not fully succeed
2  CLI usage/configuration error
3  systemd/D-Bus infrastructure error
4  no free job slot
5  target job/service not found
6  unsupported host capability
```

Document them.

Do not return arbitrary `journalctl`/`systemctl` exit codes directly as public API unless explicitly mapped.

For `sj wait`, use `1` if the waited job fails.

---

# 40. Error handling

Errors MUST be actionable.

Bad:

```text
failed
```

Good:

```text
sj: cannot start job: all 8 slots are occupied
```

Good:

```text
sj: job 3 no longer exists; this CLI does not keep persistent task history
```

Good:

```text
sj: pause requires systemd freeze/thaw on cgroup v2
```

When D-Bus returns known systemd errors, map them to domain errors.

Examples:

```text
ErrUnitExists
ErrNoSuchUnit
ErrPermission
ErrNoFreeSlots
ErrUnsupported
```

---

# 41. Safety and namespace isolation

Every destructive bulk command MUST only target units owned by the configured namespace:

```text
sj-run-*
sj-svc-*
```

Never use a broad glob that could affect unrelated user units.

Validate unit names before performing stop/kill/reset.

`sj reset` must not stop arbitrary services supplied through user-controlled malformed names.

---

# 42. Environment handling

## 42.1 `run`

Environment inheritance comes from the process invocation plus optional `direnv`.

Do not print or serialize the whole environment in debug output.

Environment can contain secrets.

## 42.2 `exec`

Preserve current environment where systemd transient execution requires explicit environment propagation.

Be deliberate: a user manager does not automatically equal the caller's transient shell environment.

The implementation MUST test what environment is inherited through `StartTransientUnit`.

If environment variables need to be explicitly transferred:

- transfer the intended caller environment
- allow denylisting system/internal variables if necessary
- never log secret values
- preserve `PATH`
- preserve locale
- preserve relevant development environment variables

Direnv mode remains preferred when available.

This area MUST receive integration tests.

---

# 43. Linger

Long-running `systemd --user` processes may need user lingering to survive logout depending on host/session configuration.

`sj doctor` reports linger state.

Documentation SHOULD explain:

```bash
loginctl enable-linger "$USER"
```

Do not silently enable it.

---

# 44. JSON stability

Machine-readable output is a first-class API.

Every JSON-producing command MUST include:

```json
{
  "schema_version": 1
}
```

or return a documented stable array/object schema containing a version.

Rules:

- add fields compatibly
- do not rename/remove fields without schema version bump
- timestamps use RFC3339 with timezone
- durations SHOULD be integer milliseconds or nanoseconds in JSON, not formatted human strings
- command argv SHOULD be an array where known
- never emit ANSI
- never mix logs/diagnostics into JSON stdout

---

# 45. Human status rendering

Human rendering SHOULD borrow Pueue's strengths:

- compact
- readable at a glance
- task label more prominent than raw systemd internals
- exact raw systemd fields available through JSON or verbose mode

Recommended columns:

```text
ID
STATE
LABEL
AGE/NEXT
UNIT
```

`--verbose` MAY add:

```text
PID
EXIT
INVOCATION
WORKDIR
```

Do not overload the default table.

---

# 46. Completion

Provide:

```text
sj completions bash
sj completions zsh
sj completions fish
sj completions nushell
```

Dynamic job/service completion SHOULD be implemented if the CLI framework allows it without major complexity.

Completion candidates:

```text
3   Unit tests
4   Build hufu
```

Completion must never make destructive changes.

---

# 47. Package layout

Recommended Go layout:

```text
cmd/sj/
    main.go

internal/cli/
    root.go
    run.go
    exec.go
    status.go
    log.go
    follow.go
    wait.go
    stop.go
    kill.go
    pause.go
    restart.go
    clean.go
    reset.go
    note.go
    service.go
    prompt.go
    doctor.go

internal/systemd/
    manager.go
    transient.go
    properties.go
    state.go
    timer.go
    freeze.go
    errors.go
    naming.go

internal/jobs/
    allocator.go
    job.go
    state.go
    query.go

internal/runner/
    shell.go
    exec.go
    direnv.go
    localnu.go

internal/journal/
    journal.go
    invocation.go
    follow.go

internal/config/
    config.go

internal/render/
    status.go
    json.go
    color.go

internal/platform/
    capabilities.go
    linger.go
```

Do not force this exact tree if a simpler equivalent emerges, but preserve separation of:

- CLI parsing
- domain logic
- systemd adapter
- journal adapter
- rendering

---

# 48. Interfaces for testability

Define narrow interfaces.

Example:

```go
type SystemdManager interface {
    StartJob(ctx context.Context, spec JobSpec) (StartResult, error)
    StopUnit(ctx context.Context, unit string) error
    ResetFailedUnit(ctx context.Context, unit string) error
    KillUnit(ctx context.Context, unit string, sig syscall.Signal) error
    FreezeUnit(ctx context.Context, unit string) error
    ThawUnit(ctx context.Context, unit string) error

    GetUnit(ctx context.Context, unit string) (UnitInfo, error)
    ListManagedUnits(ctx context.Context) ([]UnitInfo, error)
}
```

Example journal abstraction:

```go
type Journal interface {
    Read(ctx context.Context, q LogQuery) ([]byte, error)
    Follow(ctx context.Context, q LogQuery) error
}
```

Do not leak `dbus.Variant` throughout CLI/domain code.

Normalize D-Bus data inside the systemd package.

---

# 49. Domain types

Suggested:

```go
type JobID int

type JobState string

const (
    StateFree      JobState = "free"
    StateScheduled JobState = "scheduled"
    StateStarting  JobState = "starting"
    StateRunning   JobState = "running"
    StatePaused    JobState = "paused"
    StateSuccess   JobState = "success"
    StateFailed    JobState = "failed"
    StateStopping  JobState = "stopping"
    StateUnknown   JobState = "unknown"
)

type Job struct {
    ID               JobID
    Unit             string
    TimerUnit        string
    State            JobState
    ActiveState      string
    SubState         string
    Description      string
    WorkingDirectory string
    InvocationID     string
    MainPID          int
    Result           string
    ExecMainStatus   int
    StartedAt        *time.Time
    FinishedAt       *time.Time
}
```

---

# 50. Race and concurrency requirements

The following scenarios MUST work safely:

### Two terminals start jobs simultaneously

Both must either:

- receive different slots, or
- one gets `no free slots`

They MUST NOT accidentally replace each other's unit.

### `stop` races with natural process exit

Result must converge to a free/clean state.

### `clean` races with process completion

Must not stop an unrelated new invocation that reused a slot after cleanup discovered the old one.

Where possible, include InvocationID/unit-state revalidation before destructive cleanup.

### `follow` while unit exits

Must terminate naturally after journald stream completes or user interrupts.

### scheduled timer activates while `stop` runs

Must leave neither an orphan timer nor unintended long-running service.

Add integration tests for these races.

---

# 51. No "double clean" workaround

The implementation is not accepted if it requires:

```text
scan -> stop -> scan -> stop
```

as normal behavior.

A single `sj reset --force` MUST leave every finite slot available when it returns successfully.

If asynchronous systemd transitions require waiting, wait for the relevant job/state completion with a bounded timeout.

---

# 52. Timeouts

All D-Bus control operations SHOULD accept a context.

Default management timeout:

```text
5s-10s
```

Stopping a process may use the service's normal systemd stop timeout.

CLI infrastructure operations MUST not hang forever.

`follow` and `wait` are intentionally long-lived user commands and are exempt.

---

# 53. Duration parsing

Support at minimum:

```text
30s
5m
2h
24h
```

Optionally:

```text
1d
```

Absolute `--at` input MUST support RFC3339.

Human-friendly local formats MAY be added, but avoid a large natural-language date parser in v1.

Examples:

```bash
sj run --at '2026-09-08T02:00:00+08:00' 'backup'
sj note --after 30m 'check backup'
```

---

# 54. Local timezone

Human-rendered absolute times use local timezone.

JSON uses RFC3339 offset timestamps.

Never serialize ambiguous timezone-less timestamps in machine output.

---

# 55. `local.nu`

Rules:

- only applicable to `run`
- detected relative to effective working directory
- exact filename configurable
- source only if regular readable file
- `--no-local` disables it
- absence is not an error

Construct script as:

```nu
source local.nu
<original script>
```

Do not read and inline the file contents into the command.

Let Nushell source it.

---

# 56. direnv

Mode:

```text
auto
always
never
```

`auto`:

- if executable exists, use it
- otherwise run without it

`always`:

- missing executable is an error

`never`:

- never use direnv

Wrap as:

```text
direnv exec <cwd> <actual executable> <actual argv...>
```

Do not pass through an extra shell.

---

# 57. Logging correctness requirement

The current Nushell strategy of:

```text
reverse logs
take lines until "systemd ... Starting"
reverse again
```

is explicitly forbidden.

Tests MUST demonstrate that logs work regardless of:

- system locale
- message text changes
- command itself printing the word "Starting"
- repeated invocations of the same slot

Prefer `_SYSTEMD_INVOCATION_ID`.

---

# 58. Named service logs

Named service log commands use the current/latest invocation.

Examples:

```bash
sj service log ollama
sj service follow ollama
```

If systemd has restarted the service multiple times under `Restart=on-failure`, default behavior SHOULD target the current/latest invocation.

Provide:

```text
--unit-all
```

or equivalent only if needed later.

Do not overbuild historical restart navigation in v1.

---

# 59. Resource control — extension point only

Architecture SHOULD allow future transient properties such as:

```text
CPUQuota
MemoryMax
IOWeight
Nice
AllowedCPUs
```

but these are not required for MVP.

Do not build a generic property language in v1.

---

# 60. Security considerations

- never run user commands by constructing an unescaped intermediate shell command unless command is explicitly shell-mode
- `exec` MUST preserve exact argv
- do not log environment values
- sanitize/escape service names
- bulk actions only affect `sj` namespace
- do not use sudo
- do not write root unit files
- do not expose secrets in debug traces
- descriptions may contain arbitrary text; handle safely in terminal output

---

# 61. Minimum supported systemd

Determine a practical minimum at implementation time based on required D-Bus APIs.

Because freeze/thaw was added later than basic transient units, capability detection is preferred over requiring the newest systemd globally.

The program SHOULD:

- work for core run/status/log/stop on older supported systemd
- disable `pause/resume` when Freeze/Thaw is unavailable
- show capability through `sj doctor`

Do not detect capabilities solely by parsing version numbers when an API probe is more reliable.

---

# 62. Go requirements

Use a currently supported stable Go release available in the development environment.

Requirements:

- `go fmt`
- `go vet`
- unit tests
- race tests for in-process concurrency-sensitive logic where useful
- static single-binary deployment where practical
- no CGO requirement

Prefer standard library unless a dependency clearly reduces complexity.

Core external dependencies should remain small.

Expected dependencies:

```text
github.com/alecthomas/kong
github.com/coreos/go-systemd/v22
```

A TOML library may be added if needed.

---

# 63. Testing strategy

## 63.1 Unit tests

Must cover:

- unit naming/escaping
- numeric ID parsing
- state normalization
- duration parsing
- config precedence
- command construction
- Nushell script construction
- direnv wrapping
- exact exec argv preservation
- JSON rendering
- signal parsing
- error mapping
- free-slot iteration logic

## 63.2 Fake manager tests

Use fake `SystemdManager` to cover:

- first slot free
- first N slots occupied
- all slots occupied
- non-collision D-Bus error aborts allocation
- stop/reset behavior
- scheduled state
- failed state
- restart limitations

## 63.3 Linux integration tests

Run only when systemd user manager is available.

Use a test namespace such as:

```text
sjtest-<pid>-...
```

Never collide with real `sj` units.

Integration tests:

1. start `sleep 1`
2. status shows running
3. wait returns success
4. successful job remains inspectable until clean
5. clean makes the slot reusable
6. a successful `--collect` job automatically makes its slot reusable
7. failed `/bin/sh -c 'exit 7'` remains inspectable
8. log contains known output
9. invocation isolation excludes previous run
10. stop terminates `sleep 300`
11. two concurrent starts receive distinct slots
12. no-free-slot returns defined exit code
13. delayed timer enters scheduled state
14. timer eventually starts service
15. reset removes timer and service
16. named service restart works
15. freeze/thaw if supported
16. direnv integration when test dependency exists
17. `local.nu` affects `run`
18. `local.nu` does not affect `exec`

## 63.4 Shell integration tests

At minimum:

```text
bash
nushell
```

Verify:

```bash
id=$(sj run --print-id 'printf hello')
```

outputs only numeric ID.

---

# 64. Acceptance scenarios

## Scenario A — basic background task

```bash
id=$(sj run --print-id -l tests 'go test ./...')
sj status "$id"
sj follow "$id"
sj wait "$id"
```

Acceptance:

- ID is numeric
- task survives closing the terminal
- output exists in journal
- follow exiting does not stop job
- wait reflects success/failure

## Scenario B — concurrent allocation

With 4 slots:

```text
terminal A -> sj run sleep...
terminal B -> sj run sleep...
terminal C -> sj run sleep...
```

All allocations are unique.

No slot replacement.

## Scenario C — failure retention

```bash
id=$(sj exec --print-id -- sh -c 'echo failure; exit 42')
```

After completion:

```bash
sj status "$id"
```

shows failed.

```bash
sj log "$id"
```

contains `failure`.

```bash
sj clean
```

releases the slot.

## Scenario D — delayed task

```bash
id=$(sj run --print-id --delay 10m 'backup')
```

Status shows:

```text
scheduled
```

The slot cannot be reused until timer is stopped/completed.

## Scenario E — long-running service

```bash
sj service run dev -- ./server
sj service status dev
sj service follow dev
sj service restart dev
sj service stop dev
```

No finite job slot is consumed.

## Scenario F — shell semantics

Working directory contains:

```nu
# local.nu
$env.TEST_FROM_LOCAL = "yes"
```

Then:

```bash
sj run 'echo $env.TEST_FROM_LOCAL'
```

log must output:

```text
yes
```

`sj exec` MUST NOT source `local.nu`.

---

# 65. Performance goals

Normal commands should feel instantaneous.

Targets are guidance, not hard realtime requirements:

```text
sj prompt       < 100 ms typical
sj status       < 200 ms typical
sj run submit   < 300 ms typical excluding process startup
```

Avoid:

- querying every journal for status
- spawning multiple `systemctl` processes
- unnecessary filesystem scans
- querying unit properties one by one if a batched D-Bus operation is available

---

# 66. Pueue compatibility philosophy

Do not attempt wire/API compatibility with Pueue.

The similarity is UX only.

Borrow:

```text
status
log
follow
wait
restart
clean
label
JSON
```

Do not borrow:

```text
daemon
queue
stash/enqueue
dependency scheduler
priority
groups
persistent TaskID DB
```

Document the major semantic difference:

> Pueue task IDs identify persistent queue records. `sj` numeric job IDs identify reusable systemd execution slots.

---

# 67. Migration from existing Nushell functions

Map old commands:

| Existing Nushell | New `sj` |
|---|---|
| `run` | `sj run` |
| exact argv use | `sj exec` |
| `note` | `sj note` |
| `log` | `sj log` |
| `log -f` | `sj follow` |
| `all-log` | `sj log --all` or status/log combination |
| `show` | `sj status ID --verbose` |
| `stop` | `sj stop` |
| old destructive `clean` | `sj reset --force` |
| `running-units` | `sj status` |
| `bg-running` | `sj prompt` |
| `sr` | `sj sr` |

Do not preserve old unit names by default.

A migration alias/prefix option may be added later if needed.

---

# 68. Documentation requirements

Repository MUST contain:

```text
README.md
docs/architecture.md
docs/cli.md
```

README should explain:

- what `sj` is
- why systemd is the supervisor
- Pueue-inspired UX
- finite jobs vs named services
- no internal queue
- linger
- quick examples

Architecture doc should explain:

- D-Bus
- transient units
- journald
- slot allocation
- InvocationID
- state normalization
- timer model

CLI doc should be generated/kept synchronized with actual flags.

---

# 69. Implementation phases

The coding agent MUST work in phases and keep each phase buildable/testable.

## Phase 0 — skeleton and contracts

Deliver:

- Go module
- CLI parser
- config loader
- domain types
- unit naming
- interfaces
- tests
- no real process execution required yet

Acceptance:

```text
go test ./...
go vet ./...
```

pass.

## Phase 1 — core finite jobs

Implement:

```text
run
exec
status
stop
clean
reset
```

with real user-systemd D-Bus.

Must include atomic slot allocation.

No timer support yet.

## Phase 2 — journald

Implement:

```text
log
follow
wait
```

with InvocationID filtering.

Prove repeated slot reuse does not mix invocation logs.

## Phase 3 — shell adapters

Implement:

```text
local.nu
direnv
working-directory
```

and environment tests.

## Phase 4 — timer/note

Implement:

```text
--delay
--at
note
```

including race-safe slot reservation.

## Phase 5 — named services

Implement:

```text
sj service ...
```

with restart policy.

## Phase 6 — process control

Implement:

```text
kill
pause
resume
restart
```

with capability detection.

## Phase 7 — UX hardening

Implement:

```text
prompt
doctor
JSON schemas
completions
color
documentation
```

Run full integration suite.

---

# 70. Coding-agent execution rules

The coding agent SHOULD:

1. read this specification completely before editing
2. inspect the repository before choosing file layout
3. implement one phase at a time
4. run tests after every phase
5. avoid speculative abstractions
6. avoid adding a daemon
7. avoid adding persistent queue state
8. prefer D-Bus to parsing systemctl output
9. keep journal handling separate from systemd state handling
10. report any requirement that cannot be implemented cleanly before replacing it with a different architecture

The coding agent MUST NOT:

- silently convert the design into a Pueue clone
- introduce SQLite in v1
- add a scheduler
- make `sj` itself a systemd service
- parse `"Starting"` journal text to delimit invocations
- implement `find-free-then-start`
- require the user to run cleanup twice
- use string-joined argv for `exec`
- affect non-`sj` user units during bulk cleanup

---

# 71. Definition of Done

v1 is complete when all of the following are true:

- [ ] single Go binary
- [ ] no daemon
- [ ] user-systemd D-Bus connection
- [ ] finite background jobs
- [ ] race-safe job slot allocation
- [ ] exact-argv `exec`
- [ ] Nushell `run`
- [ ] optional `local.nu`
- [ ] optional `direnv`
- [ ] status table
- [ ] stable status JSON
- [ ] InvocationID-based logs
- [ ] follow
- [ ] wait
- [ ] graceful stop
- [ ] signal kill
- [ ] clean
- [ ] one-pass destructive reset
- [ ] delayed timers
- [ ] notes
- [ ] named long-running transient services
- [ ] pause/resume capability handling
- [ ] prompt output
- [ ] doctor
- [ ] shell completions
- [ ] unit tests
- [ ] Linux systemd integration tests
- [ ] documentation
- [ ] no persistent scheduler state
- [ ] no daemon

---

# 72. Future extensions — not v1

The architecture may later support:

```text
CPUQuota
MemoryMax
Nice
IOWeight
GPU device constraints
service templates
task history
SQLite metadata
retry policy
queueing
group concurrency
dependency DAG
remote hosts
web/TUI
```

Any future queue/history feature must be designed as a new capability rather than smuggling scheduler state into the v1 CLI.

---

# 73. Reference projects and documentation

Use these as design/reference material, not code to blindly copy:

- Pueue: https://github.com/Nukesor/pueue
- systemd D-Bus API: https://www.freedesktop.org/software/systemd/man/latest/org.freedesktop.systemd1.html
- systemd-run: https://www.freedesktop.org/software/systemd/man/latest/systemd-run.html
- systemctl: https://www.freedesktop.org/software/systemd/man/latest/systemctl.html
- journalctl: https://www.freedesktop.org/software/systemd/man/latest/journalctl.html
- systemd.service: https://www.freedesktop.org/software/systemd/man/latest/systemd.service.html
- systemd.timer: https://www.freedesktop.org/software/systemd/man/latest/systemd.timer.html
- go-systemd: https://github.com/coreos/go-systemd
- Kong: https://github.com/alecthomas/kong

---

# 74. Final architectural invariant

When there is uncertainty, use this invariant to decide:

```text
                systemd --user
                 /        \
          runtime state   process supervision
               |
sj CLI --------+
  |
  +---------- journald
  |
  `---------- shell adapters
```

`sj` is an ergonomic controller.

It is **not** the scheduler, process supervisor, persistent task database, or logging daemon.

That separation is the main design goal of the project.
