# CLI reference

```
sj run [options] [-c|--collect] SCRIPT...
sj exec [options] [-c|--collect] -- COMMAND [ARG...]
sj status [ID...] [--json] [--running] [--failed] [--free]
sj show ID
sj log [ID...] [--lines N|--full]
sj follow [ID]
sj wait ID... | --all
sj stop|kill|pause|resume ID... | --all
sj restart [ID...]
sj clean [--failed-only] [--dry-run] [--all]
sj reset [--services] [--force] [--dry-run]
sj note [--after DURATION|--at RFC3339] [--ttl DURATION] TEXT
sj service run NAME [--restart POLICY] [--restart-sec DURATION] -- COMMAND...
sj service status|list [NAME...]
sj service log|follow NAME
sj service stop|restart|kill|pause|resume NAME...
sj prompt [--json|--tmux] [--strict]
sj doctor
sj completions bash|zsh|fish|nushell
sj shell-run|sr -- COMMAND...
```

Global `--color auto|always|never` is accepted before the command. JSON output
uses `schema_version: 1`; diagnostics are always written to stderr. `run
--print-id` writes only the numeric slot ID on stdout.

`sj prompt --tmux` emits a detailed, single-line status fragment. It includes
slot usage and state counts followed by each job's ID, state, current elapsed
time, PID, and working-directory basename when those fields are available.
Success and failure use `✅` and `❌`; completion age and exit-code text are
omitted.
Scheduled jobs only show their state and label. For example:

```text
sj(▶1◷1🔴1/8)│①▶build·5m12s·p1234·@project│②◷backup│③❌tests·@project
```

The summary uses `🔴` and `🟢` for failed and successful counts, while
individual jobs use `❌` and `✅`. Job slots use Unicode circled digits (`①` to
`⑳`) and fall back to ordinary numbers beyond that range. Compact markers are
`p` for PID and `@` for the working directory.

When the detailed line exceeds 45 terminal columns, it automatically compresses to
slot plus status, for example:

```text
sj│①✅│②❌│③⏳│④⏰
```

Configure tmux with enough room for the detailed fragment:

```tmux
set -g status-interval 2
set -g status-right-length 120
set -g status-right '#(sj prompt --tmux)'
```

Prompt errors remain silent by default so a missing user systemd session does
not damage the tmux status line; add `--strict` while diagnosing failures.

Completed `run` and `exec` jobs remain inspectable until `clean` or `reset` by
default. `-c`/`--collect` automatically releases a successful job after it
exits; failed jobs remain inspectable.

Starting a job or named service waits up to 30 seconds for systemd's startup
result before reporting success. It does not wait for the task to finish.
For `--type notify`, startup includes the service's readiness notification.
A startup timeout does not cancel the unit: inspect its status before retrying.
Delayed jobs confirm timer startup; the command's eventual result is available
through `status` and `wait`. Timers use 1 ms accuracy, subject to host scheduling.

`--at` accepts RFC3339 timestamps with a timezone, or local
`YYYY-MM-DD HH:MM[:SS]` values. Timestamps are converted to systemd calendar
syntax in UTC before submission.

Exit codes are: 0 success, 1 managed job failure, 2 usage/configuration error,
3 systemd or D-Bus failure, 4 no free slot, 5 missing target, and 6 unsupported
host capability.

Configuration is read from `$SJ_CONFIG`, then
`$XDG_CONFIG_HOME/sj/config.toml` (or `~/.config/sj/config.toml`). Supported
keys are `max_jobs`, `unit_prefix`, `shell`, `source_local_nu`,
`local_nu_filename`, `direnv`, `default_log_lines`, `multi_log_lines`,
`service_restart`, `service_restart_sec`, and `color`. `SJ_MAX_JOBS`,
`SJ_SHELL`, `SJ_DIRENV`, and `SJ_COLOR` override the file.
