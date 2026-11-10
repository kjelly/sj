package main

import (
	"cmp"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	osuser "os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	systemd "github.com/coreos/go-systemd/v22/dbus"
	"github.com/godbus/dbus/v5"
	"sj/internal/core"
)

const prefix = "sj"

type app struct {
	conn              *systemd.Conn
	max               int
	shell             string
	direnv            string
	local             bool
	localFile         string
	config            string
	prefix            string
	logLines          int
	multiLogLines     int
	serviceRestart    string
	serviceRestartSec time.Duration
	color             string
	exitStatus        int
}

// durationFlag accepts the duration syntax documented by sj, including the
// optional day suffix (for example, 1d), and implements both flag.Value and
// pflag.Value for the command adapters.
type durationFlag struct {
	value time.Duration
}

func (d *durationFlag) String() string { return d.value.String() }
func (d *durationFlag) Type() string   { return "duration" }
func (d *durationFlag) Set(raw string) error {
	v, err := core.ParseDuration(raw)
	if err != nil {
		return err
	}
	d.value = v
	return nil
}

func main() {
	os.Exit(newApp().run(os.Args[1:]))
}
func fail(code int, err error) int { fmt.Fprintln(os.Stderr, "sj:", err); return code }
func (a *app) loadConfig() error {
	path := os.Getenv("SJ_CONFIG")
	if path == "" {
		path = filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "sj", "config.toml")
		if os.Getenv("XDG_CONFIG_HOME") == "" {
			h, _ := os.UserHomeDir()
			path = filepath.Join(h, ".config", "sj", "config.toml")
		}
	}
	a.config = path
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		b = nil
	} else if err != nil {
		return err
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(p) != 2 {
			return fmt.Errorf("invalid config line in %s: %q", path, line)
		}
		k, err := parseConfigValue(p[0])
		if err != nil {
			return fmt.Errorf("invalid config line in %s: %q", path, line)
		}
		v, err := parseConfigValue(p[1])
		if err != nil {
			return fmt.Errorf("invalid config line in %s: %q", path, line)
		}
		switch k {
		case "max_jobs":
			n, e := strconv.Atoi(v)
			if e != nil || n < 1 {
				return fmt.Errorf("invalid max_jobs in %s", path)
			}
			a.max = n
		case "shell":
			a.shell = v
		case "direnv":
			if v != "auto" && v != "always" && v != "never" {
				return fmt.Errorf("invalid direnv in %s", path)
			}
			a.direnv = v
		case "unit_prefix":
			if !validPrefix(v) {
				return fmt.Errorf("invalid unit_prefix in %s", path)
			}
			a.prefix = v
		case "source_local_nu":
			value, e := strconv.ParseBool(v)
			if e != nil {
				return fmt.Errorf("invalid source_local_nu in %s", path)
			}
			a.local = value
		case "local_nu_filename":
			if v == "" || filepath.Base(v) != v {
				return fmt.Errorf("invalid local_nu_filename in %s", path)
			}
			a.localFile = v
		case "default_log_lines":
			n, e := strconv.Atoi(v)
			if e != nil || n < 0 {
				return fmt.Errorf("invalid default_log_lines in %s", path)
			}
			a.logLines = n
		case "multi_log_lines":
			n, e := strconv.Atoi(v)
			if e != nil || n < 0 {
				return fmt.Errorf("invalid multi_log_lines in %s", path)
			}
			a.multiLogLines = n
		case "service_restart":
			if !validRestart(v) {
				return fmt.Errorf("invalid service_restart in %s", path)
			}
			a.serviceRestart = v
		case "service_restart_sec":
			d, e := core.ParseDuration(v)
			if e != nil {
				return fmt.Errorf("invalid service_restart_sec in %s", path)
			}
			a.serviceRestartSec = d
		case "color":
			if v != "auto" && v != "always" && v != "never" {
				return fmt.Errorf("invalid color in %s", path)
			}
			a.color = v
		default:
			return fmt.Errorf("unknown config key %q in %s", k, path)
		}
	}
	if v := os.Getenv("SJ_MAX_JOBS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return fmt.Errorf("invalid SJ_MAX_JOBS")
		}
		a.max = n
	}
	if v := os.Getenv("SJ_SHELL"); v != "" {
		a.shell = v
	}
	if v := os.Getenv("SJ_DIRENV"); v != "" {
		if v != "auto" && v != "always" && v != "never" {
			return fmt.Errorf("invalid SJ_DIRENV")
		}
		a.direnv = v
	}
	if v := os.Getenv("SJ_COLOR"); v != "" {
		if v != "auto" && v != "always" && v != "never" {
			return fmt.Errorf("invalid SJ_COLOR")
		}
		a.color = v
	}
	if _, ok := os.LookupEnv("NO_COLOR"); ok && a.color == "auto" {
		a.color = "never"
	}
	return nil
}

// parseConfigValue handles the small scalar subset used by sj's TOML config.
// It intentionally rejects malformed quoting instead of silently accepting a
// value with trailing syntax.
func parseConfigValue(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("empty value")
	}
	quoted := raw[0] == '"' || raw[0] == '\''
	if quoted {
		quote := raw[0]
		end := strings.IndexByte(raw[1:], quote)
		if end < 0 {
			return "", errors.New("unterminated string")
		}
		end++
		if strings.TrimSpace(raw[end+1:]) != "" && !strings.HasPrefix(strings.TrimSpace(raw[end+1:]), "#") {
			return "", errors.New("trailing characters")
		}
		return raw[1:end], nil
	}
	if i := strings.IndexByte(raw, '#'); i >= 0 {
		raw = strings.TrimSpace(raw[:i])
	}
	if raw == "" {
		return "", errors.New("empty value")
	}
	return raw, nil
}

func validPrefix(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || (i > 0 && r >= '0' && r <= '9') || (i > 0 && r == '-') {
			continue
		}
		return false
	}
	return true
}

// systemdEnvironment removes assignments whose names systemd cannot accept.
// Unlike os.Setenv, a process environment inherited through exec may contain
// names such as "docker-cli" that are legal to the kernel but invalid in a
// systemd Environment= property.
func systemdEnvironment(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, assignment := range environ {
		name, _, ok := strings.Cut(assignment, "=")
		if ok && validEnvironmentName(name) {
			out = append(out, assignment)
		}
	}
	return out
}

func validEnvironmentName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

func validRestart(s string) bool {
	return slices.Contains([]string{"no", "on-success", "on-failure", "on-abnormal", "on-watchdog", "on-abort", "always"}, s)
}

func (a *app) runJob(args []string, exact bool) int {
	hasDelimiter := slices.Contains(args, "--")
	fs := flag.NewFlagSet("job", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	label := fs.String("label", "", "description")
	fs.StringVar(label, "l", "", "description")
	fs.StringVar(label, "doc", "", "description")
	work := fs.String("working-directory", ".", "working directory")
	fs.StringVar(work, "w", ".", "working directory")
	printID := fs.Bool("print-id", false, "print only id")
	follow := fs.Bool("follow", false, "follow logs")
	quiet := fs.Bool("quiet", false, "suppress success output")
	collect := fs.Bool("collect", false, "automatically collect the job after successful completion")
	noDirenv := fs.Bool("no-direnv", false, "disable direnv")
	var noLocal bool
	var shell string
	if !exact {
		fs.BoolVar(&noLocal, "no-local", false, "disable local.nu")
		fs.StringVar(&shell, "shell", a.shell, "shell")
	}
	var delay durationFlag
	fs.Var(&delay, "delay", "delay")
	fs.Var(&delay, "d", "delay")
	at := fs.String("at", "", "RFC3339 time")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if delay.value != 0 && *at != "" {
		return fail(2, errors.New("--delay and --at are mutually exclusive"))
	}
	if delay.value < 0 {
		return fail(2, errors.New("--delay must be positive"))
	}
	if *follow && (delay.value != 0 || *at != "") {
		return fail(2, errors.New("--follow cannot be used with a delayed job"))
	}
	dir, err := core.ResolveDir(*work)
	if err != nil {
		return fail(2, err)
	}
	rest := fs.Args()
	if exact {
		if !hasDelimiter {
			return fail(2, errors.New("exec requires -- COMMAND [ARG...]"))
		}
		if len(rest) == 0 {
			return fail(2, errors.New("exec requires a command"))
		}
	} else if len(rest) == 0 {
		return fail(2, errors.New("run requires a script"))
	}
	var argv []string
	if exact {
		argv = core.BuildExecArgv(rest)
	} else {
		script := strings.Join(rest, " ")
		local := filepath.Join(dir, a.localFile)
		argv = core.BuildRunArgv(shell, core.BuildScript(script, local, a.local && !noLocal && regular(local)))
	}
	if !*noDirenv && a.direnv != "never" && (a.direnv == "always" || core.Executable("direnv")) {
		argv = core.WrapDirenv(dir, argv)
	} else if a.direnv == "always" && !core.Executable("direnv") {
		return fail(2, errors.New("direnv is required but not installed"))
	}
	if *label == "" {
		*label = core.TruncateLabel(strings.Join(rest, " "), 80)
	}
	id, err := a.startJob(argv, dir, *label, delay.value, *at, !*collect)
	if err != nil {
		if errors.Is(err, core.ErrNoFreeSlots) {
			return fail(4, fmt.Errorf("cannot start job: all %d slots are occupied", a.max))
		}
		return fail(3, err)
	}
	if *printID {
		fmt.Println(id)
	} else if !*follow && !*quiet {
		fmt.Printf("Started job %d: %s\n", id, *label)
	}
	if *follow {
		return a.logs([]string{strconv.Itoa(int(id))}, true)
	}
	return 0
}
func regular(path string) bool { st, err := os.Stat(path); return err == nil && st.Mode().IsRegular() }
func prop(name string, value any) systemd.Property {
	return systemd.Property{Name: name, Value: dbus.MakeVariant(value)}
}
func (a *app) startJob(argv []string, dir, label string, delay time.Duration, at string, retain bool) (core.JobID, error) {
	return core.AllocateSlot(a.max, func(id core.JobID) error {
		name := core.UnitName(a.prefix, id)
		if delay != 0 || at != "" {
			if err := a.startDelayed(id, argv, dir, label, delay, at, retain); err == nil {
				return nil
			} else if isUnitExists(err) {
				return core.ErrUnitExists
			} else {
				return err
			}
		}
		p := a.jobProperties(argv, dir, label, retain)
		err := startTransientUnit(name, p, nil)
		if err == nil {
			return nil
		}
		if isUnitExists(err) {
			return core.ErrUnitExists
		}
		return err
	})
}

func (a *app) jobProperties(argv []string, dir, label string, retain bool) []systemd.Property {
	properties := []systemd.Property{systemd.PropDescription(label), systemd.PropExecStart(argv, false), prop("Type", "exec"), prop("WorkingDirectory", dir), prop("CollectMode", "inactive"), prop("Environment", systemdEnvironment(os.Environ()))}
	if retain {
		// Keep successful run/exec units inspectable and their slots reserved
		// until the user explicitly releases them with clean or reset.
		properties = append(properties, prop("RemainAfterExit", true))
	}
	return properties
}

func (a *app) startDelayed(id core.JobID, argv []string, dir, label string, delay time.Duration, at string, retain bool) error {
	timerProps, err := timerProperties(label, delay, at)
	if err != nil {
		return err
	}
	service := core.UnitName(a.prefix, id)
	timer := core.TimerName(a.prefix, id)
	// systemd infers the service from the timer's matching basename. Unit is
	// a read-only D-Bus property and cannot be set on a transient timer.

	// StartTransientUnit's auxiliary-unit argument makes reservation of the
	// timer and service one manager transaction.  In particular, never create a
	// timer first and later replace a service in the same numbered slot.
	return startTransientUnit(timer, timerProps, []systemd.PropertyCollection{{Name: service, Properties: a.jobProperties(argv, dir, label, retain)}})
}

func isUnitExistsText(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UnitExists") ||
		strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "already loaded")
}

func timerDelay(delay time.Duration, at string) (time.Duration, error) {
	if delay > 0 {
		return delay, nil
	}
	if at == "" {
		return 0, errors.New("a delayed job requires --delay or --at")
	}
	when, err := parseAtTime(at)
	if err != nil {
		return 0, fmt.Errorf("invalid --at time %q: %w", at, err)
	}
	if !when.After(time.Now()) {
		return 0, errors.New("--at must be in the future")
	}
	return time.Until(when), nil
}

// parseAtTime accepts the documented RFC3339 form and the convenient
// date-time form used in the specification examples. A timezone-less value
// is interpreted in the local timezone, matching normal CLI expectations.
func parseAtTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if when, err := time.Parse(time.RFC3339, raw); err == nil {
		return when, nil
	}
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02 15:04:05"} {
		if when, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return when, nil
		}
	}
	return time.Time{}, errors.New("expected RFC3339 or YYYY-MM-DD HH:MM[:SS]")
}

func isUnitExists(err error) bool {
	if err == nil {
		return false
	}
	var val dbus.Error
	if errors.As(err, &val) && val.Name == "org.freedesktop.systemd1.UnitExists" {
		return true
	}
	var ptr *dbus.Error
	if errors.As(err, &ptr) && ptr != nil && ptr.Name == "org.freedesktop.systemd1.UnitExists" {
		return true
	}
	return isUnitExistsText(err)
}

func (a *app) note(args []string) int {
	fs := flag.NewFlagSet("note", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var after durationFlag
	fs.Var(&after, "after", "delay before showing the note")
	fs.Var(&after, "a", "delay before showing the note")
	at := fs.String("at", "", "RFC3339 time")
	var ttl durationFlag
	fs.Var(&ttl, "ttl", "how long the note remains active")
	fs.Var(&ttl, "t", "how long the note remains active")
	label := fs.String("label", "", "description")
	fs.StringVar(label, "l", "", "description")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if after.value != 0 && *at != "" {
		return fail(2, errors.New("--after and --at are mutually exclusive"))
	}
	if after.value < 0 || ttl.value < 0 {
		return fail(2, errors.New("--after and --ttl must be positive"))
	}
	if len(fs.Args()) == 0 {
		return fail(2, errors.New("note requires text"))
	}
	text := strings.Join(fs.Args(), " ")
	if *label == "" {
		*label = "📓" + core.TruncateLabel(text, 78)
	} else if !strings.HasPrefix(*label, "📓") {
		*label = "📓" + *label
	}
	dir, err := core.ResolveDir(".")
	if err != nil {
		return fail(2, err)
	}
	duration := ttl.value
	argv := noteArgv(duration)
	id, err := a.startJob(argv, dir, *label, after.value, *at, false)
	if err != nil {
		return fail(3, err)
	}
	fmt.Println(id)
	return 0
}

func noteArgv(ttl time.Duration) []string {
	if ttl <= 0 {
		// Keep an untimed note active without relying on an unusually large
		// duration being accepted by the platform's sleep implementation.
		return []string{"/bin/tail", "-f", "/dev/null"}
	}
	return []string{"/bin/sleep", ttl.String()}
}

func (a *app) serviceRun(args []string) int {
	if len(args) == 0 {
		return fail(2, errors.New("service run requires a name"))
	}
	name := args[0]
	if name == "" || strings.ContainsAny(name, "/\\") {
		return fail(2, errors.New("service name may not contain a path separator"))
	}
	fs := flag.NewFlagSet("service run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	work := fs.String("working-directory", ".", "working directory")
	description := fs.String("label", name, "description")
	restart := fs.String("restart", a.serviceRestart, "restart policy")
	restartSec := fs.Duration("restart-sec", a.serviceRestartSec, "restart delay")
	typeName := fs.String("type", "exec", "service type")
	hasDelimiter := slices.Contains(args[1:], "--")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	rest := fs.Args()
	if !hasDelimiter || len(rest) == 0 {
		return fail(2, errors.New("service run requires -- COMMAND [ARG...]"))
	}
	if !validRestart(*restart) {
		return fail(2, fmt.Errorf("invalid restart policy %q", *restart))
	}
	if *typeName != "exec" && *typeName != "notify" {
		return fail(2, fmt.Errorf("invalid service type %q", *typeName))
	}
	if *restartSec < 0 {
		return fail(2, errors.New("--restart-sec must not be negative"))
	}
	dir, err := core.ResolveDir(*work)
	if err != nil {
		return fail(2, err)
	}
	unit := core.ServiceName(a.prefix, name)
	props := []systemd.Property{systemd.PropDescription(*description), systemd.PropExecStart(rest, false), prop("WorkingDirectory", dir), prop("Type", *typeName), prop("Restart", *restart), prop("RestartUSec", uint64((*restartSec)/time.Microsecond)), prop("Environment", systemdEnvironment(os.Environ()))}
	if *typeName == "notify" {
		props = append(props, prop("NotifyAccess", "main"))
	}
	if err := startTransientUnit(unit, props, nil); err != nil {
		return fail(3, err)
	}
	fmt.Printf("Started service %s\n", name)
	return 0
}

func (a *app) serviceStatus(args []string) int {
	units, err := a.conn.ListUnitsByPatternsContext(context.Background(), nil, []string{a.prefix + "-svc-*.service"})
	if err != nil {
		return fail(3, err)
	}
	if units == nil {
		units = []systemd.UnitStatus{}
	}
	jsonOut := contains(args, "--json")
	requested := make(map[string]bool)
	for _, arg := range args {
		if arg == "--json" {
			continue
		}
		if arg == "" || strings.ContainsAny(arg, "/\\") {
			return fail(2, fmt.Errorf("invalid service name %q", arg))
		}
		requested[core.ServiceName(a.prefix, arg)] = true
	}
	if len(requested) > 0 {
		units = slices.DeleteFunc(units, func(unit systemd.UnitStatus) bool { return !requested[unit.Name] })
	}
	if jsonOut {
		return printJSON(map[string]any{"schema_version": 1, "services": units})
	}
	fmt.Println("STATE\tDESCRIPTION\tUNIT")
	for _, u := range units {
		fmt.Printf("%s\t%s\t%s\n", u.ActiveState, u.Description, u.Name)
	}
	return 0
}

func (a *app) serviceLogs(args []string, follow bool) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return fail(2, errors.New("service log requires a name"))
	}
	return a.logsUnit(core.ServiceName(a.prefix, args[0]), args[1:], follow)
}

func (a *app) serviceControl(args []string, action string) int {
	args = reorderFlags(args, "signal")
	fs := flag.NewFlagSet("service "+action, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	signalName := fs.String("signal", "SIGTERM", "signal for kill")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	args = fs.Args()
	if len(args) == 0 {
		return fail(2, fmt.Errorf("service %s requires a name", action))
	}
	sig, err := core.ParseSignal(*signalName)
	if err != nil {
		return fail(2, err)
	}
	for _, name := range args {
		if name == "" || strings.HasPrefix(name, "-") || strings.ContainsAny(name, "/\\") {
			return fail(2, fmt.Errorf("invalid service name %q", name))
		}
		unit := core.ServiceName(a.prefix, name)
		var err error
		switch action {
		case "stop":
			_, err = a.conn.StopUnitContext(context.Background(), unit, "replace", nil)
		case "restart":
			_, err = a.conn.RestartUnitContext(context.Background(), unit, "replace", nil)
		case "kill":
			err = a.conn.KillUnitWithTarget(context.Background(), unit, systemd.All, int32(sig))
		case "pause":
			err = a.conn.FreezeUnit(context.Background(), unit)
		case "resume":
			err = a.conn.ThawUnit(context.Background(), unit)
		}
		if err != nil {
			if action == "pause" || action == "resume" {
				return fail(6, fmt.Errorf("%s requires systemd freeze/thaw on cgroup v2: %w", action, err))
			}
			return fail(3, err)
		}
	}
	return 0
}

func (a *app) restart(args []string) int {
	if len(args) == 0 {
		jobs, err := a.jobs()
		if err != nil {
			return fail(3, err)
		}
		for _, j := range jobs {
			if j.State == core.Success || j.State == core.Failed {
				if _, err := a.conn.RestartUnitContext(context.Background(), j.Unit, "replace", nil); err != nil {
					return fail(3, err)
				}
			}
		}
		return 0
	}
	for _, id := range args {
		n, err := core.ParseID(id)
		if err != nil {
			return fail(2, err)
		}
		unit := core.UnitName(a.prefix, n)
		if jobs, e := a.jobs(); e == nil {
			for _, j := range jobs {
				if j.ID == n && j.TimerUnit != "" {
					unit = j.TimerUnit
					break
				}
			}
		}
		if _, err := a.conn.RestartUnitContext(context.Background(), unit, "replace", nil); err != nil {
			return fail(3, err)
		}
	}
	return 0
}

func (a *app) jobs() ([]core.Job, error) {
	units, err := a.conn.ListUnitsByPatternsContext(context.Background(), nil, []string{a.prefix + "-run-*.service"})
	if err != nil {
		return nil, err
	}
	timers, err := a.conn.ListUnitsByPatternsContext(context.Background(), nil, []string{a.prefix + "-run-*.timer"})
	if err != nil {
		return nil, err
	}
	byID := make(map[core.JobID]int, len(units))
	out := make([]core.Job, 0, len(units)+len(timers))
	for _, u := range units {
		var id core.JobID
		if _, err := fmt.Sscanf(u.Name, a.prefix+"-run-%d.service", &id); err != nil {
			continue
		}
		if id < 1 {
			continue
		}
		j := core.Job{ID: id, Unit: u.Name, Kind: "job", ActiveState: u.ActiveState, SubState: u.SubState, Description: u.Description}
		j.InvocationID, _ = a.unitInvocation(u.Name)
		for _, q := range []struct {
			name string
			dst  *string
		}{{"Result", &j.Result}, {"WorkingDirectory", &j.WorkingDirectory}} {
			if p, e := a.conn.GetServicePropertyContext(context.Background(), u.Name, q.name); e == nil {
				if s, ok := p.Value.Value().(string); ok {
					*q.dst = s
				}
			}
		}
		freezer := ""
		if p, e := a.conn.GetUnitPropertyContext(context.Background(), u.Name, "FreezerState"); e == nil {
			if s, ok := p.Value.Value().(string); ok {
				freezer = s
			}
		}
		j.State = core.Normalize(u.ActiveState, u.SubState, freezer, j.Result)
		if p, e := a.conn.GetServicePropertyContext(context.Background(), u.Name, "MainPID"); e == nil {
			if n, ok := p.Value.Value().(uint32); ok {
				j.MainPID = n
			}
		}
		if p, e := a.conn.GetServicePropertyContext(context.Background(), u.Name, "ExecMainStatus"); e == nil {
			if n, ok := p.Value.Value().(int32); ok {
				j.ExecMainStatus = n
			}
		}
		j.StartedAt = a.unitTimestamp(u.Name, "ActiveEnterTimestamp")
		j.FinishedAt = a.unitTimestamp(u.Name, "InactiveEnterTimestamp")
		byID[id] = len(out)
		out = append(out, j)
	}
	return mergeTimers(a.prefix, out, byID, timers), nil
}

func mergeTimers(prefix string, out []core.Job, byID map[core.JobID]int, timers []systemd.UnitStatus) []core.Job {
	for _, u := range timers {
		var id core.JobID
		if _, err := fmt.Sscanf(u.Name, prefix+"-run-%d.timer", &id); err != nil {
			continue
		}
		if id < 1 {
			continue
		}
		if i, ok := byID[id]; ok {
			out[i].TimerUnit = u.Name
			if u.ActiveState == "active" && u.SubState == "waiting" {
				out[i].State = core.Scheduled
			}
			continue
		}
		state := core.Normalize(u.ActiveState, u.SubState, "", "")
		if u.ActiveState == "active" && u.SubState == "waiting" {
			state = core.Scheduled
		} else if u.ActiveState == "active" && u.SubState == "elapsed" {
			// A collected service may disappear before its timer is unloaded.
			// An elapsed timer alone is not evidence of the service's result.
			state = core.Unknown
		}
		out = append(out, core.Job{ID: id, Unit: core.UnitName(prefix, id), TimerUnit: u.Name, Kind: "job", State: state, ActiveState: u.ActiveState, SubState: u.SubState, Description: u.Description})
	}
	slices.SortFunc(out, func(left, right core.Job) int { return cmp.Compare(left.ID, right.ID) })
	return out
}

func (a *app) unitTimestamp(unit, property string) *time.Time {
	p, err := a.conn.GetUnitPropertyContext(context.Background(), unit, property)
	if err != nil {
		return nil
	}
	usec, ok := p.Value.Value().(uint64)
	if !ok || usec == 0 {
		return nil
	}
	return new(time.UnixMicro(int64(usec)))
}

func (a *app) status(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOut := fs.Bool("json", false, "JSON output")
	all := fs.Bool("all", false, "include all managed units")
	running := fs.Bool("running", false, "show running jobs")
	failed := fs.Bool("failed", false, "show failed jobs")
	free := fs.Bool("free", false, "show free slots")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	jobs, err := a.jobs()
	if err != nil {
		return fail(3, err)
	}
	occupied := make(map[core.JobID]bool, len(jobs))
	for _, j := range jobs {
		occupied[j.ID] = true
	}
	selected := make(map[core.JobID]bool)
	for _, raw := range fs.Args() {
		id, err := core.ParseID(raw)
		if err != nil {
			return fail(2, err)
		}
		selected[id] = true
	}
	filtered := jobs[:0]
	seen := make(map[core.JobID]bool, len(selected))
	for _, j := range jobs {
		if len(selected) > 0 && !selected[j.ID] {
			continue
		}
		seen[j.ID] = true
		if *running && j.State != core.Running && j.State != core.Starting {
			continue
		}
		if *failed && j.State != core.Failed {
			continue
		}
		filtered = append(filtered, j)
	}
	jobs = filtered
	for id := range selected {
		if !seen[id] {
			return fail(5, fmt.Errorf("job %d is no longer loaded; sj does not keep persistent task history", id))
		}
	}
	services := []systemd.UnitStatus{}
	if *all {
		services, err = a.conn.ListUnitsByPatternsContext(context.Background(), nil, []string{a.prefix + "-svc-*.service"})
		if err != nil {
			return fail(3, err)
		}
	}
	if *jsonOut {
		return printJSON(map[string]any{"schema_version": 1, "jobs": jobs, "services": services})
	}
	fmt.Println("ID\tSTATE\tLABEL\tUNIT")
	for _, j := range jobs {
		fmt.Printf("%d\t%s\t%s\t%s\n", j.ID, j.State, j.Description, j.Unit)
	}
	if *free {
		for id := 1; id <= a.max; id++ {
			if !occupied[core.JobID(id)] {
				fmt.Printf("%d\tfree\t\t\n", id)
			}
		}
	}
	_ = all // all is the default; retained for script-compatible CLI semantics.
	return 0
}

func (a *app) show(args []string) int {
	if len(args) != 1 {
		return fail(2, errors.New("show requires exactly one job ID"))
	}
	id, err := core.ParseID(args[0])
	if err != nil {
		return fail(2, err)
	}
	jobs, err := a.jobs()
	if err != nil {
		return fail(3, err)
	}
	for _, job := range jobs {
		if job.ID == id {
			fmt.Println(showJobContent(job.Description))
			return 0
		}
	}
	return fail(5, fmt.Errorf("job %d is no longer loaded; sj does not keep persistent task history", id))
}

func showJobContent(description string) string {
	return strings.TrimPrefix(description, "📓")
}

func printJSON(v any) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fail(3, err)
	}
	return 0
}
func contains(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}

func (a *app) logs(args []string, follow bool) int {
	all := contains(args, "--all")
	failedOnly := contains(args, "--failed")
	jsonOut := contains(args, "--json")
	ids := make([]string, 0)
	for i := 0; i < len(args); i++ {
		x := args[i]
		switch {
		case x == "--all" || x == "--failed" || x == "--full" || x == "--json":
		case x == "--lines":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return fail(2, errors.New("--lines requires a value"))
			}
			i++
		case strings.HasPrefix(x, "--lines="):
		case strings.HasPrefix(x, "-"):
			return fail(2, fmt.Errorf("unknown log option %q", x))
		default:
			ids = append(ids, x)
		}
	}
	if all || failedOnly {
		jobs, err := a.jobs()
		if err != nil {
			return fail(3, err)
		}
		for _, j := range jobs {
			if all || j.State == core.Failed {
				ids = append(ids, strconv.Itoa(int(j.ID)))
			}
		}
	}
	if follow && len(ids) == 0 {
		jobs, err := a.jobs()
		if err != nil {
			return fail(3, err)
		}
		for _, j := range jobs {
			if j.State == core.Running || j.State == core.Starting {
				if len(ids) > 0 {
					return fail(2, errors.New("follow requires an ID when more than one job is running"))
				}
				ids = append(ids, strconv.Itoa(int(j.ID)))
			}
		}
		if len(ids) == 0 {
			return fail(2, errors.New("follow requires exactly one running job"))
		}
	}
	if follow && len(ids) > 1 {
		return fail(2, errors.New("follow accepts exactly one job ID"))
	}
	if !follow && len(ids) == 0 {
		jobs, err := a.jobs()
		if err != nil {
			return fail(3, err)
		}
		for _, j := range jobs {
			ids = append(ids, strconv.Itoa(int(j.ID)))
		}
	}
	if len(ids) == 0 {
		return 0
	}
	if jsonOut {
		items := make([]map[string]any, 0, len(ids))
		for _, id := range ids {
			n, err := core.ParseID(id)
			if err != nil {
				return fail(2, err)
			}
			jobUnit := core.UnitName(a.prefix, n)
			invocation, _ := a.unitInvocation(jobUnit)
			items = append(items, map[string]any{"id": n, "unit": jobUnit, "invocation_id": invocation})
		}
		return printJSON(map[string]any{"schema_version": 1, "jobs": items})
	}
	defaultLines := a.logLines
	if len(ids) > 1 {
		defaultLines = a.multiLogLines
	}
	for _, id := range ids {
		n, err := core.ParseID(id)
		if err != nil {
			return fail(2, err)
		}
		if err := a.logsUnitErrWithDefault(core.UnitName(a.prefix, n), args, follow, defaultLines); err != nil {
			return exitCode(err)
		}
		if follow {
			break
		}
	}
	return 0
}

func (a *app) logsUnit(unit string, args []string, follow bool) int {
	if err := a.logsUnitErr(unit, args, follow); err != nil {
		return exitCode(err)
	}
	return 0
}

func (a *app) logsUnitErr(unit string, args []string, follow bool) error {
	return a.logsUnitErrWithDefault(unit, args, follow, a.logLines)
}

func (a *app) logsUnitErrWithDefault(unit string, args []string, follow bool, defaultLines int) error {
	lines := strconv.Itoa(defaultLines)
	for i, x := range args {
		switch {
		case x == "--full":
			lines = ""
		case strings.HasPrefix(x, "--lines="):
			lines = strings.TrimPrefix(x, "--lines=")
		case x == "--lines" && i+1 < len(args):
			lines = args[i+1]
		}
	}
	if lines != "" {
		n, err := strconv.Atoi(lines)
		if err != nil || n < 0 {
			return fmt.Errorf("invalid --lines value %q", lines)
		}
	}
	argv := []string{"--user", "--no-pager", "--output=cat", "--no-hostname"}
	if lines != "" {
		argv = append(argv, "-n", lines)
	}
	if follow {
		argv = append(argv, "-f")
	}
	if unit != "" {
		argv = append(argv, "-u", unit)
		if invocation, err := a.unitInvocation(unit); err == nil && invocation != "" {
			argv = append(argv, "_SYSTEMD_INVOCATION_ID="+invocation)
		}
	}
	cmd := exec.Command("journalctl", argv...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (a *app) unitInvocation(unit string) (string, error) {
	p, err := a.conn.GetUnitPropertyContext(context.Background(), unit, "InvocationID")
	if err != nil {
		return "", err
	}
	switch v := p.Value.Value().(type) {
	case string:
		return v, nil
	case []byte:
		return hex.EncodeToString(v), nil
	default:
		return "", nil
	}
}
func (a *app) wait(args []string) int {
	all := contains(args, "--all")
	quiet := contains(args, "--quiet")
	if all || quiet {
		filtered := args[:0]
		for _, arg := range args {
			if arg != "--all" && arg != "--quiet" {
				filtered = append(filtered, arg)
			}
		}
		args = filtered
	}
	if !all && len(args) == 0 {
		return fail(2, errors.New("wait requires one or more job IDs or --all"))
	}
	wanted := make(map[string]bool, len(args))
	observed := make(map[string]core.State, len(args))
	for _, arg := range args {
		wanted[arg] = true
	}
	for {
		jobs, err := a.jobs()
		if err != nil {
			return fail(3, err)
		}
		all := true
		failed := false
		seen := make(map[string]bool, len(wanted))
		for _, j := range jobs {
			id := strconv.Itoa(int(j.ID))
			if len(args) > 0 && !wanted[id] {
				continue
			}
			seen[id] = true
			observed[id] = j.State
			if j.State != core.Success && j.State != core.Failed {
				all = false
			}
			failed = failed || j.State == core.Failed
		}
		for id := range wanted {
			if !seen[id] {
				// A concurrent clean/reset may remove a retained unit after wait has
				// observed its success. That is still a successful terminal state.
				if observed[id] == core.Success {
					continue
				}
				return fail(3, fmt.Errorf("job %s is no longer loaded", id))
			}
		}
		if all && len(jobs) == 0 {
			return 0
		}
		if all {
			if failed {
				return 1
			}
			return 0
		}
		time.Sleep(500 * time.Millisecond)
	}
}
func (a *app) control(args []string, action string) int {
	args = reorderFlags(args, "signal", "all")
	fs := flag.NewFlagSet(action, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	signalName := fs.String("signal", "SIGTERM", "signal for kill")
	all := fs.Bool("all", false, "target all managed jobs")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	sig, err := core.ParseSignal(*signalName)
	if err != nil {
		return fail(2, err)
	}
	targets := fs.Args()
	if *all {
		jobs, e := a.jobs()
		if e != nil {
			return fail(3, e)
		}
		targets = targets[:0]
		for _, j := range jobs {
			targets = append(targets, strconv.Itoa(int(j.ID)))
		}
	}
	if len(targets) == 0 {
		return fail(2, fmt.Errorf("%s requires one or more job IDs or --all", action))
	}
	var failures []error
	for _, s := range targets {
		n, err := core.ParseID(s)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		unit := core.UnitName(a.prefix, n)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		var e error
		switch action {
		case "stop":
			_, timerErr := a.conn.StopUnitContext(ctx, core.TimerName(a.prefix, n), "replace", nil)
			_, serviceErr := a.conn.StopUnitContext(ctx, unit, "replace", nil)
			if !isNoSuchUnit(timerErr) && timerErr != nil {
				e = timerErr
			} else if !isNoSuchUnit(serviceErr) && serviceErr != nil {
				e = serviceErr
			} else if isNoSuchUnit(timerErr) && isNoSuchUnit(serviceErr) {
				fmt.Fprintf(os.Stderr, "sj: job %d is already free\n", n)
			} else {
				// A stopped failed service remains loaded until its failed state is
				// reset, which is precisely what makes the slot reusable.
				e = a.conn.ResetFailedUnitContext(ctx, unit)
				if isNoSuchUnit(e) {
					e = nil
				}
			}
		case "kill":
			// A waiting timer must be stopped as well, otherwise it can start
			// the service again after the requested kill.
			_, timerErr := a.conn.StopUnitContext(ctx, core.TimerName(a.prefix, n), "replace", nil)
			if !isNoSuchUnit(timerErr) && timerErr != nil {
				e = timerErr
				break
			}
			e = a.conn.KillUnitWithTarget(ctx, unit, systemd.All, int32(sig))
		case "pause":
			e = a.conn.FreezeUnit(ctx, unit)
		case "resume":
			e = a.conn.ThawUnit(ctx, unit)
		}
		cancel()
		if e != nil {
			if action == "pause" || action == "resume" {
				failures = append(failures, fmt.Errorf("%s requires systemd freeze/thaw on cgroup v2: %w", action, e))
				continue
			}
			failures = append(failures, e)
		}
	}
	if len(failures) > 0 {
		for _, err := range failures {
			fmt.Fprintln(os.Stderr, "sj:", err)
		}
		for _, err := range failures {
			if action == "pause" || action == "resume" {
				return 6
			}
			if isNoSuchUnit(err) {
				return 5
			}
		}
		return 3
	}
	return 0
}

// reorderFlags permits the user-friendly form "kill 3 --signal SIGKILL"
// even though the standard flag package stops parsing at the first operand.
func reorderFlags(args []string, names ...string) []string {
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[name] = true
	}
	flags := make([]string, 0, len(args))
	operands := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		x := args[i]
		name := strings.TrimPrefix(x, "--")
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			name = name[:eq]
		}
		if !wanted[name] {
			operands = append(operands, x)
			continue
		}
		flags = append(flags, x)
		if !strings.ContainsRune(x, '=') && name == "signal" && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, operands...)
}

func isNoSuchUnit(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "NoSuchUnit") || strings.Contains(err.Error(), "not loaded"))
}

func isNoteDescription(desc string) bool {
	return strings.HasPrefix(desc, "📓")
}

func isNote(j core.Job) bool {
	return isNoteDescription(j.Description)
}

func shouldCleanJob(kind string, j core.Job, failedOnly, all bool) bool {
	if kind == "reset" {
		return true
	}
	if isNote(j) {
		if !all {
			return false
		}
		if failedOnly {
			return j.State == core.Failed
		}
		return true
	}
	if failedOnly {
		return j.State == core.Failed
	}
	return j.State == core.Failed || j.State == core.Success
}

func (a *app) clean(kind string, args []string) int {
	fs := flag.NewFlagSet(kind, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	failedOnly := fs.Bool("failed-only", false, "only failed jobs")
	dryRun := fs.Bool("dry-run", false, "report without changing units")
	force := fs.Bool("force", false, "confirm destructive reset")
	services := fs.Bool("services", false, "include named services")
	all := fs.Bool("all", false, "include notes in cleanup")
	fs.BoolVar(all, "a", false, "include notes in cleanup")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if len(fs.Args()) != 0 {
		return fail(2, fmt.Errorf("%s does not accept positional arguments", kind))
	}
	if kind == "reset" && !*dryRun && !*force && !isTerminal(os.Stdin) {
		return fail(2, errors.New("sj reset requires --force in non-interactive mode"))
	}
	if kind == "reset" && !*dryRun && !*force && isTerminal(os.Stdin) {
		fmt.Fprint(os.Stderr, "Stop every managed finite job? [y/N] ")
		var answer string
		if _, err := fmt.Fscanln(os.Stdin, &answer); err != nil || strings.ToLower(answer) != "y" {
			return fail(2, errors.New("reset cancelled"))
		}
	}
	jobs, err := a.jobs()
	if err != nil {
		return fail(3, err)
	}
	cleaned := 0
	for _, j := range jobs {
		if !shouldCleanJob(kind, j, *failedOnly, *all) {
			continue
		}
		if *dryRun {
			fmt.Printf("%s\t%s\n", kind, j.Unit)
			continue
		}
		cleaned++
		if j.TimerUnit != "" {
			if _, e := a.conn.StopUnitContext(context.Background(), j.TimerUnit, "replace", nil); e != nil && !isNoSuchUnit(e) {
				return fail(3, e)
			}
		}
		if _, e := a.conn.StopUnitContext(context.Background(), j.Unit, "replace", nil); e != nil && !isNoSuchUnit(e) {
			return fail(3, e)
		}
		if e := a.conn.ResetFailedUnitContext(context.Background(), j.Unit); e != nil && !isNoSuchUnit(e) {
			return fail(3, e)
		}
	}
	if kind == "reset" && *services && !*dryRun {
		units, e := a.conn.ListUnitsByPatternsContext(context.Background(), nil, []string{a.prefix + "-svc-*.service"})
		if e != nil {
			return fail(3, e)
		}
		for _, u := range units {
			if _, e = a.conn.StopUnitContext(context.Background(), u.Name, "replace", nil); e != nil && !isNoSuchUnit(e) {
				return fail(3, e)
			}
		}
	}
	if kind == "reset" && !*dryRun {
		// Stop/reset are asynchronous D-Bus jobs. Wait briefly for the manager
		// to settle so a successful reset is immediately reusable in one pass.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			remaining, e := a.jobs()
			if e != nil {
				return fail(3, e)
			}
			if len(remaining) == 0 {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	if !*dryRun {
		if kind == "clean" {
			fmt.Printf("Cleaned %d job%s.\n", cleaned, plural(cleaned))
		} else {
			fmt.Printf("Reset %d job%s.\n", cleaned, plural(cleaned))
		}
	}
	return 0
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
func (a *app) doctor(args []string) int {
	fixLinger := contains(args, "--fix-linger")
	for _, arg := range args {
		if arg != "--fix-linger" {
			return fail(2, fmt.Errorf("unknown doctor option %q", arg))
		}
	}
	if fixLinger {
		currentUser := os.Getenv("USER")
		if currentUser == "" {
			if u, err := osuser.Current(); err == nil {
				currentUser = u.Username
			}
		}
		if currentUser == "" {
			return fail(2, errors.New("cannot determine current user for linger"))
		}
		cmd := exec.Command("loginctl", "enable-linger", currentUser)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fail(3, fmt.Errorf("enable linger: %w", err))
		}
	}
	version := "unknown"
	if value, err := a.conn.GetManagerProperty("Version"); err == nil {
		version = strings.Trim(value, `"`)
	}
	cgroup := "v1"
	freeze := "NO"
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err == nil {
		cgroup, freeze = "v2", "available (manager will verify per unit)"
	}
	linger := "unknown"
	if u := os.Getenv("USER"); u != "" {
		cmd := exec.Command("loginctl", "show-user", u, "-p", "Linger", "--value")
		if out, err := cmd.Output(); err == nil {
			switch strings.TrimSpace(string(out)) {
			case "yes":
				linger = "enabled"
			case "no":
				linger = "disabled"
			}
		}
	}
	fmt.Printf("systemd user manager\tOK\nD-Bus user connection\tOK\nsystemd version\t%s\ncgroup version\t%s\nfreeze/thaw support\t%s\nlinger\t%s\nworking config\t%s\nmax job slots\t%d\n", version, cgroup, freeze, linger, a.config, a.max)
	if core.Executable("journalctl") {
		fmt.Println("journalctl\tOK")
	} else {
		fmt.Println("journalctl\tmissing")
	}
	for _, name := range []string{"nu", "direnv"} {
		if path, err := exec.LookPath(name); err == nil {
			fmt.Printf("%s\t%s\n", name, path)
		} else {
			fmt.Printf("%s\tmissing\n", name)
		}
	}
	return 0
}
func (a *app) shellRun(args []string) int {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return fail(2, errors.New("shell-run requires a command"))
	}
	c := exec.Command("systemd-run", append([]string{"--user", "--pty", "--pipe", "--collect"}, args...)...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return exitCode(err)
	}
	return 0
}

func exitCode(err error) int {
	var e *exec.ExitError
	if errors.As(err, &e) {
		// journalctl/systemd helper exit statuses are implementation details;
		// expose them as infrastructure failures rather than leaking arbitrary
		// values through sj's stable public exit-code contract.
		if e.ExitCode() == 130 {
			return 1
		}
		return 3
	}
	return 3
}
func completions(args []string) int {
	if len(args) != 1 {
		return fail(2, errors.New("choose bash, zsh, fish, or nushell"))
	}
	commands := "run exec status show log follow wait stop kill pause resume restart clean reset note service prompt doctor completions shell-run sr"
	switch args[0] {
	case "bash":
		fmt.Printf("_sj_complete(){ COMPREPLY=( $(compgen -W '%s' -- \"${COMP_WORDS[COMP_CWORD]}\") ); }; complete -F _sj_complete sj\n", commands)
	case "zsh":
		fmt.Printf("#compdef sj\n_arguments '1:command:(%s)'\n", strings.ReplaceAll(commands, " ", " "))
	case "fish":
		fmt.Printf("complete -c sj -f -a '%s'\n", commands)
	case "nushell":
		words := strings.Fields(commands)
		quoted := make([]string, len(words))
		for i, word := range words {
			quoted[i] = "'" + word + "'"
		}
		fmt.Printf("def 'nu-complete sj' [] { [ %s ] };\n", strings.Join(quoted, " "))
	default:
		return fail(2, fmt.Errorf("unsupported shell %q", args[0]))
	}
	return 0
}
