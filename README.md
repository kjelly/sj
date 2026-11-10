# sj

`sj` is a small, systemd-native user job manager. It provides Pueue-like
commands for finite background jobs while leaving supervision, runtime state,
and logs to `systemd --user` and journald. It has no daemon, queue, or task
database.

Finite jobs use reusable numbered slots (`sj-run-1.service`, and so on).
Completed jobs remain available for inspection and consume their slots until
`sj clean` or `sj reset`. Pass `--collect` (`-c`) to automatically release a
successful job instead. Named services (`sj service run NAME -- COMMAND`) are
independent of that pool.

## Install on Linux

Download the archive matching your CPU from
[GitHub Releases](https://github.com/kjelly/sj/releases/latest):
`linux_amd64` for x86-64 or `linux_arm64` for ARM64. Release binaries are
statically linked and require no Go installation.

For example, install the first release on x86-64:

```bash
mkdir -p ~/.local/bin
curl -fLO https://github.com/kjelly/sj/releases/download/v0.1.0/sj_v0.1.0_linux_amd64.tar.gz
curl -fLO https://github.com/kjelly/sj/releases/download/v0.1.0/SHA256SUMS
sha256sum --ignore-missing -c SHA256SUMS
tar -xzf sj_v0.1.0_linux_amd64.tar.gz
install -m 755 sj_v0.1.0_linux_amd64/sj ~/.local/bin/sj
```

Ensure `~/.local/bin` is in your `PATH`. The host must provide a working
`systemd --user` manager and `journalctl`. `sj run` defaults to Nushell (`nu`);
use `sj run --shell bash 'COMMAND'` or `sj exec -- COMMAND ARG...` as needed.

## Build and release

Build from source with Go 1.26 or later:

```bash
go build -o sj ./cmd/sj
```

Push a version tag to run tests, build Linux amd64/arm64 archives, and publish
a GitHub release with SHA-256 checksums:

```bash
git tag v0.1.1
git push origin v0.1.1
```

Tags containing a hyphen publish prereleases. To build the same archives locally,
run `scripts/build-release.sh v0.1.1 dist`. CI runs race tests against a real
systemd user manager before a release can be published.

## Quick start

```bash
sj run -l tests 'go test ./...'
sj exec -- rsync -a 'source directory' /backup/
sj status
sj follow 1
sj wait 1
sj run --delay 30m 'backup-script'
sj service run dev-web -- ./server --port 8080
```

Show a detailed live summary in the tmux status line:

```tmux
set -g status-interval 2
set -g status-right-length 120
set -g status-right '#(sj prompt --tmux)'
```

`run` executes the script through Nushell by default. `exec` preserves every
argument exactly and never invokes a shell. When enabled, `run` sources a
regular `local.nu` in its working directory and wraps commands with
`direnv exec`; neither behavior applies implicitly to `exec`.

Long-lived user services may need lingering to survive logout. Inspect the
host with `sj doctor`; enable it explicitly when appropriate:

```bash
loginctl enable-linger "$USER"
```

## Cobra CLI

`sj` uses `github.com/spf13/cobra` for its root command and subcommands. The
existing commands are available through Cobra, including command-specific help:

```bash
sj --help
sj run --help
sj status --help
sj service --help
sj service run --help
```

The `--color auto|always|never` option is a persistent global flag and may be
placed before a subcommand. Cobra handles command validation and help; job
execution, systemd integration, journald logs, and the existing JSON output
remain unchanged.

See [architecture](docs/architecture.md) and the [CLI reference](docs/cli.md).
