package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	systemd "github.com/coreos/go-systemd/v22/dbus"
	"github.com/spf13/cobra"
)

type cliError struct {
	code int
	err  error
}

func (e *cliError) Error() string { return e.err.Error() }
func (e *cliError) Unwrap() error { return e.err }

func newApp() *app {
	return &app{
		max:               8,
		shell:             "nu",
		direnv:            "auto",
		local:             true,
		localFile:         "local.nu",
		prefix:            prefix,
		logLines:          50,
		multiLogLines:     5,
		serviceRestart:    "on-failure",
		serviceRestartSec: 2 * time.Second,
		color:             "auto",
	}
}

func (a *app) run(args []string) int {
	root := a.rootCommand()
	root.SetArgs(args)
	defer func() {
		if a.conn != nil {
			a.conn.Close()
		}
	}()
	if err := root.Execute(); err != nil {
		var commandErr *cliError
		if errors.As(err, &commandErr) {
			return fail(commandErr.code, commandErr.err)
		}
		return fail(2, err)
	}
	return a.exitStatus
}

func (a *app) rootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "sj",
		Short:         "systemd-native user job manager",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = cmd.Usage()
			a.exitStatus = 2
			return nil
		},
		PersistentPreRunE: a.prepare,
	}
	root.SetOut(os.Stdout)
	root.SetErr(os.Stderr)
	root.CompletionOptions.DisableDefaultCmd = true
	root.PersistentFlags().StringVar(&a.color, "color", a.color, "color output: auto, always, or never")

	root.AddCommand(
		a.jobCommand(false),
		a.jobCommand(true),
		a.noteCommand(),
		a.statusCommand(),
		a.showCommand(),
		a.logsCommand(false),
		a.logsCommand(true),
		a.waitCommand(),
		a.controlCommand("stop"),
		a.controlCommand("kill"),
		a.controlCommand("pause"),
		a.controlCommand("resume"),
		a.restartCommand(),
		a.cleanCommand("clean"),
		a.cleanCommand("reset"),
		a.serviceCommand(),
		a.doctorCommand(),
		a.promptCommand(),
		a.completionsCommand(),
		a.shellRunCommand(),
	)
	return root
}

func (a *app) prepare(cmd *cobra.Command, _ []string) error {
	if cmd == cmd.Root() {
		return nil
	}
	colorOverride := a.color
	colorChanged := cmd.Flags().Changed("color")
	if err := a.loadConfig(); err != nil {
		return &cliError{code: 2, err: err}
	}
	if colorChanged {
		a.color = colorOverride
	}
	if a.color != "auto" && a.color != "always" && a.color != "never" {
		return &cliError{code: 2, err: fmt.Errorf("invalid color %q", a.color)}
	}
	// Completion generation is useful on hosts without a systemd user manager.
	if cmd.Name() == "completions" {
		return nil
	}
	// NewUserConnectionContext keeps the supplied context attached to the
	// lifetime of the returned D-Bus connection. Do not use a timeout context
	// here and cancel it after a successful connection: cancellation would
	// close the connection before the command can use it. The app owns the
	// connection and closes it in run after command execution completes.
	conn, err := systemdUserConnection(context.Background())
	if err != nil {
		return &cliError{code: 3, err: fmt.Errorf("cannot connect to systemd user manager: %w", err)}
	}
	a.conn = conn
	return nil
}

func systemdUserConnection(ctx context.Context) (*systemd.Conn, error) {
	return systemd.NewUserConnectionContext(ctx)
}

func (a *app) legacyRun(fn func([]string) int) func(*cobra.Command, []string) error {
	return func(_ *cobra.Command, args []string) error {
		a.exitStatus = fn(args)
		return nil
	}
}

func (a *app) jobCommand(exact bool) *cobra.Command {
	use := "run [options] SCRIPT..."
	short := "run a finite job through the configured shell"
	if exact {
		use = "exec [options] -- COMMAND [ARG...]"
		short = "run a finite job without invoking a shell"
	}
	var label, work, shell, at string
	var printID, follow, quiet, collect, noDirenv, noLocal bool
	var delay durationFlag
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			legacy := make([]string, 0, len(args)+12)
			if cmd.Flags().Changed("label") {
				legacy = append(legacy, "--label", label)
			}
			if cmd.Flags().Changed("working-directory") {
				legacy = append(legacy, "--working-directory", work)
			}
			if cmd.Flags().Changed("print-id") && printID {
				legacy = append(legacy, "--print-id")
			}
			if cmd.Flags().Changed("follow") && follow {
				legacy = append(legacy, "--follow")
			}
			if cmd.Flags().Changed("quiet") && quiet {
				legacy = append(legacy, "--quiet")
			}
			if cmd.Flags().Changed("collect") && collect {
				legacy = append(legacy, "--collect")
			}
			if cmd.Flags().Changed("no-direnv") && noDirenv {
				legacy = append(legacy, "--no-direnv")
			}
			if !exact {
				if cmd.Flags().Changed("no-local") && noLocal {
					legacy = append(legacy, "--no-local")
				}
				if cmd.Flags().Changed("shell") {
					legacy = append(legacy, "--shell", shell)
				}
			}
			if cmd.Flags().Changed("delay") {
				legacy = append(legacy, "--delay", delay.String())
			}
			if cmd.Flags().Changed("at") {
				legacy = append(legacy, "--at", at)
			}
			if cmd.Flags().ArgsLenAtDash() >= 0 {
				legacy = append(legacy, "--")
			}
			legacy = append(legacy, args...)
			return a.legacyRun(func(args []string) int { return a.runJob(args, exact) })(cmd, legacy)
		},
	}
	flags := cmd.Flags()
	flags.StringVarP(&label, "label", "l", "", "description")
	flags.StringVar(&label, "doc", "", "description")
	flags.StringVarP(&work, "working-directory", "w", ".", "working directory")
	flags.BoolVar(&printID, "print-id", false, "print only id")
	flags.BoolVar(&follow, "follow", false, "follow logs")
	flags.BoolVar(&quiet, "quiet", false, "suppress success output")
	flags.BoolVarP(&collect, "collect", "c", false, "automatically collect the job after successful completion")
	flags.BoolVar(&noDirenv, "no-direnv", false, "disable direnv")
	if !exact {
		flags.BoolVar(&noLocal, "no-local", false, "disable local.nu")
		flags.StringVar(&shell, "shell", a.shell, "shell")
	}
	flags.VarP(&delay, "delay", "d", "delay")
	flags.StringVar(&at, "at", "", "RFC3339 time")
	return cmd
}

func (a *app) noteCommand() *cobra.Command {
	var after, ttl durationFlag
	var at, label string
	cmd := &cobra.Command{
		Use:   "note [options] TEXT",
		Short: "create a reminder represented by a systemd unit",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			legacy := make([]string, 0, len(args)+10)
			if cmd.Flags().Changed("after") {
				legacy = append(legacy, "--after", after.String())
			}
			if cmd.Flags().Changed("at") {
				legacy = append(legacy, "--at", at)
			}
			if cmd.Flags().Changed("ttl") {
				legacy = append(legacy, "--ttl", ttl.String())
			}
			if cmd.Flags().Changed("label") {
				legacy = append(legacy, "--label", label)
			}
			legacy = append(legacy, args...)
			return a.legacyRun(a.note)(cmd, legacy)
		},
	}
	cmd.Flags().VarP(&after, "after", "a", "delay before showing the note")
	cmd.Flags().StringVar(&at, "at", "", "RFC3339 time")
	cmd.Flags().VarP(&ttl, "ttl", "t", "how long the note remains active")
	cmd.Flags().StringVarP(&label, "label", "l", "", "description")
	return cmd
}

func (a *app) statusCommand() *cobra.Command {
	var jsonOut, all, running, failed, free bool
	cmd := &cobra.Command{
		Use:   "status [ID...]",
		Short: "show managed job status",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			legacy := make([]string, 0, len(args)+5)
			if jsonOut {
				legacy = append(legacy, "--json")
			}
			if all {
				legacy = append(legacy, "--all")
			}
			if running {
				legacy = append(legacy, "--running")
			}
			if failed {
				legacy = append(legacy, "--failed")
			}
			if free {
				legacy = append(legacy, "--free")
			}
			legacy = append(legacy, args...)
			return a.legacyRun(a.status)(cmd, legacy)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "JSON output")
	cmd.Flags().BoolVar(&all, "all", false, "include all managed units")
	cmd.Flags().BoolVar(&running, "running", false, "show running jobs")
	cmd.Flags().BoolVar(&failed, "failed", false, "show failed jobs")
	cmd.Flags().BoolVar(&free, "free", false, "show free slots")
	return cmd
}

func (a *app) showCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show ID",
		Short: "show job content",
		Args:  cobra.ExactArgs(1),
		RunE:  a.legacyRun(a.show),
	}
	return cmd
}

func (a *app) logsCommand(follow bool) *cobra.Command {
	use, short := "log [options] [ID...]", "show job logs"
	if follow {
		use, short = "follow [options] [ID]", "follow a job log"
	}
	var all, failed, full, jsonOut bool
	var lines int
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			legacy := make([]string, 0, len(args)+5)
			if all {
				legacy = append(legacy, "--all")
			}
			if failed {
				legacy = append(legacy, "--failed")
			}
			if full {
				legacy = append(legacy, "--full")
			}
			if jsonOut {
				legacy = append(legacy, "--json")
			}
			if cmd.Flags().Changed("lines") {
				legacy = append(legacy, "--lines", fmt.Sprint(lines))
			}
			legacy = append(legacy, args...)
			if follow {
				return a.legacyRun(func(args []string) int { return a.logs(args, true) })(cmd, legacy)
			}
			return a.legacyRun(func(args []string) int { return a.logs(args, false) })(cmd, legacy)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "show all jobs")
	cmd.Flags().BoolVar(&failed, "failed", false, "show failed jobs")
	cmd.Flags().BoolVar(&full, "full", false, "show the complete log")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "JSON output")
	cmd.Flags().IntVar(&lines, "lines", 0, "number of log lines")
	return cmd
}

func (a *app) waitCommand() *cobra.Command {
	var all, quiet bool
	cmd := &cobra.Command{
		Use:   "wait ID... | --all",
		Short: "wait for jobs to finish",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			legacy := make([]string, 0, len(args)+2)
			if all {
				legacy = append(legacy, "--all")
			}
			if quiet {
				legacy = append(legacy, "--quiet")
			}
			legacy = append(legacy, args...)
			return a.legacyRun(a.wait)(cmd, legacy)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "wait for all managed jobs")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "suppress output")
	return cmd
}

func (a *app) controlCommand(action string) *cobra.Command {
	var signal string
	var all bool
	cmd := &cobra.Command{
		Use:   action + " ID... | --all",
		Short: action + " managed jobs",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			legacy := make([]string, 0, len(args)+3)
			if cmd.Flags().Changed("signal") {
				legacy = append(legacy, "--signal", signal)
			}
			if all {
				legacy = append(legacy, "--all")
			}
			legacy = append(legacy, args...)
			return a.legacyRun(func(args []string) int { return a.control(args, action) })(cmd, legacy)
		},
	}
	cmd.Flags().StringVar(&signal, "signal", "SIGTERM", "signal for kill")
	cmd.Flags().BoolVar(&all, "all", false, "target all managed jobs")
	return cmd
}

func (a *app) restartCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "restart [ID...]",
		Short: "restart jobs or all completed jobs",
		Args:  cobra.ArbitraryArgs,
		RunE:  a.legacyRun(a.restart),
	}
}

func (a *app) cleanCommand(kind string) *cobra.Command {
	var failedOnly, dryRun, force, services, all bool
	cmd := &cobra.Command{
		Use:   kind,
		Short: kind + " managed jobs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			legacy := make([]string, 0, 5)
			if failedOnly {
				legacy = append(legacy, "--failed-only")
			}
			if dryRun {
				legacy = append(legacy, "--dry-run")
			}
			if force {
				legacy = append(legacy, "--force")
			}
			if services {
				legacy = append(legacy, "--services")
			}
			if all {
				legacy = append(legacy, "--all")
			}
			return a.legacyRun(func(args []string) int { return a.clean(kind, args) })(cmd, legacy)
		},
	}
	cmd.Flags().BoolVar(&failedOnly, "failed-only", false, "only failed jobs")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report without changing units")
	cmd.Flags().BoolVar(&force, "force", false, "confirm destructive reset")
	cmd.Flags().BoolVar(&services, "services", false, "include named services")
	cmd.Flags().BoolVarP(&all, "all", "a", false, "include notes in cleanup")
	if kind == "clean" {
		cmd.Flags().Lookup("force").Hidden = true
		cmd.Flags().Lookup("services").Hidden = true
	} else {
		cmd.Flags().Lookup("all").Hidden = true
	}
	return cmd
}

func (a *app) serviceCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "service", Short: "manage named long-running services"}
	cmd.AddCommand(a.serviceRunCommand(), a.serviceStatusCommand(), a.serviceLogsCommand(false), a.serviceLogsCommand(true))
	for _, action := range []string{"stop", "restart", "kill", "pause", "resume"} {
		cmd.AddCommand(a.serviceControlCommand(action))
	}
	list := a.serviceStatusCommand()
	list.Use = "list [NAME...]"
	list.Short = "list named services"
	cmd.AddCommand(list)
	return cmd
}

func (a *app) serviceRunCommand() *cobra.Command {
	var work, label, restart, typ string
	var restartSec time.Duration
	cmd := &cobra.Command{
		Use:   "run NAME -- COMMAND [ARG...]",
		Short: "start a named service",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			legacy := make([]string, 0, len(args)+9)
			if len(args) > 0 {
				legacy = append(legacy, args[0])
				args = args[1:]
			}
			if cmd.Flags().Changed("working-directory") {
				legacy = append(legacy, "--working-directory", work)
			}
			if cmd.Flags().Changed("label") {
				legacy = append(legacy, "--label", label)
			}
			if cmd.Flags().Changed("restart") {
				legacy = append(legacy, "--restart", restart)
			}
			if cmd.Flags().Changed("restart-sec") {
				legacy = append(legacy, "--restart-sec", restartSec.String())
			}
			if cmd.Flags().Changed("type") {
				legacy = append(legacy, "--type", typ)
			}
			if cmd.Flags().ArgsLenAtDash() >= 0 {
				legacy = append(legacy, "--")
			}
			legacy = append(legacy, args...)
			return a.legacyRun(a.serviceRun)(cmd, legacy)
		},
	}
	cmd.Flags().StringVarP(&work, "working-directory", "w", ".", "working directory")
	cmd.Flags().StringVarP(&label, "label", "l", "", "description")
	cmd.Flags().StringVar(&restart, "restart", a.serviceRestart, "restart policy")
	cmd.Flags().DurationVar(&restartSec, "restart-sec", a.serviceRestartSec, "restart delay")
	cmd.Flags().StringVar(&typ, "type", "exec", "service type: exec or notify")
	return cmd
}

func (a *app) serviceStatusCommand() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status [NAME...]",
		Short: "show named service status",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if jsonOut {
				args = append([]string{"--json"}, args...)
			}
			return a.legacyRun(a.serviceStatus)(cmd, args)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "JSON output")
	return cmd
}

func (a *app) serviceLogsCommand(follow bool) *cobra.Command {
	use, short := "log NAME [options]", "show a service log"
	if follow {
		use, short = "follow NAME [options]", "follow a service log"
	}
	var full bool
	var lines int
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			legacy := append([]string(nil), args...)
			if full {
				legacy = append(legacy, "--full")
			}
			if cmd.Flags().Changed("lines") {
				legacy = append(legacy, "--lines", fmt.Sprint(lines))
			}
			if follow {
				return a.legacyRun(func(args []string) int { return a.serviceLogs(args, true) })(cmd, legacy)
			}
			return a.legacyRun(func(args []string) int { return a.serviceLogs(args, false) })(cmd, legacy)
		},
	}
	cmd.Flags().BoolVar(&full, "full", false, "show the complete log")
	cmd.Flags().IntVar(&lines, "lines", 0, "number of log lines")
	return cmd
}

func (a *app) serviceControlCommand(action string) *cobra.Command {
	var signal string
	cmd := &cobra.Command{
		Use:   action + " NAME...",
		Short: action + " named services",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("signal") {
				args = append([]string{"--signal", signal}, args...)
			}
			return a.legacyRun(func(args []string) int { return a.serviceControl(args, action) })(cmd, args)
		},
	}
	cmd.Flags().StringVar(&signal, "signal", "SIGTERM", "signal for kill")
	return cmd
}

func (a *app) doctorCommand() *cobra.Command {
	var fixLinger bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "inspect systemd and host capabilities",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if fixLinger {
				return a.legacyRun(a.doctor)(cmd, []string{"--fix-linger"})
			}
			return a.legacyRun(a.doctor)(cmd, nil)
		},
	}
	cmd.Flags().BoolVar(&fixLinger, "fix-linger", false, "enable user lingering")
	return cmd
}

func (a *app) promptCommand() *cobra.Command {
	var jsonOut, strict, tmux bool
	cmd := &cobra.Command{
		Use:   "prompt",
		Short: "print a prompt-friendly job summary",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			args := make([]string, 0, 3)
			if jsonOut {
				args = append(args, "--json")
			}
			if strict {
				args = append(args, "--strict")
			}
			if tmux {
				args = append(args, "--tmux")
			}
			return a.legacyRun(a.prompt)(cmd, args)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "JSON output")
	cmd.Flags().BoolVar(&strict, "strict", false, "fail when status cannot be read")
	cmd.Flags().BoolVar(&tmux, "tmux", false, "detailed single-line tmux status output")
	cmd.MarkFlagsMutuallyExclusive("json", "tmux")
	return cmd
}

func (a *app) completionsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "completions bash|zsh|fish|nushell",
		Short: "generate shell completion script",
		Args:  cobra.ExactArgs(1),
		RunE:  a.legacyRun(completions),
	}
}

func (a *app) shellRunCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "shell-run|sr -- COMMAND...",
		Aliases: []string{"sr"},
		Short:   "run an interactive command under systemd",
		Args:    cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().ArgsLenAtDash() >= 0 {
				args = append([]string{"--"}, args...)
			}
			return a.legacyRun(a.shellRun)(cmd, args)
		},
	}
}
