package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/coreos/go-systemd/v22/unit"
)

type JobID int
type State string

// JobState is the descriptive name used by the public domain model. State is
// retained as a concise compatibility alias for existing callers.
type JobState = State

const (
	Free      State = "free"
	Scheduled State = "scheduled"
	Starting  State = "starting"
	Running   State = "running"
	Paused    State = "paused"
	Success   State = "success"
	Failed    State = "failed"
	Stopping  State = "stopping"
	Unknown   State = "unknown"
)

const (
	StateFree      = Free
	StateScheduled = Scheduled
	StateStarting  = Starting
	StateRunning   = Running
	StatePaused    = Paused
	StateSuccess   = Success
	StateFailed    = Failed
	StateStopping  = Stopping
	StateUnknown   = Unknown
)

var (
	ErrNoFreeSlots = errors.New("no free job slots")
	// ErrUnitExists is returned by an atomic StartTransientUnit attempt when
	// another invocation already owns the candidate slot.
	ErrUnitExists = errors.New("unit already exists")
)

// AllocateSlot centralizes the race-safe slot iteration policy. The attempt
// callback must perform an atomic create/start operation; only ErrUnitExists
// permits trying the next slot. Every other error is returned immediately.
func AllocateSlot(max int, attempt func(JobID) error) (JobID, error) {
	if max < 1 {
		return 0, ErrNoFreeSlots
	}
	for i := range max {
		id := JobID(i + 1)
		err := attempt(id)
		if err == nil {
			return id, nil
		}
		if errors.Is(err, ErrUnitExists) {
			continue
		}
		return 0, err
	}
	return 0, ErrNoFreeSlots
}

type Job struct {
	ID               JobID      `json:"id"`
	Unit             string     `json:"unit"`
	TimerUnit        string     `json:"timer_unit"`
	Kind             string     `json:"kind"`
	State            State      `json:"state"`
	ActiveState      string     `json:"active_state"`
	SubState         string     `json:"sub_state"`
	Description      string     `json:"description"`
	WorkingDirectory string     `json:"working_directory"`
	InvocationID     string     `json:"invocation_id"`
	MainPID          uint32     `json:"main_pid"`
	Result           string     `json:"result"`
	ExecMainStatus   int32      `json:"exec_main_status"`
	StartedAt        *time.Time `json:"started_at,omitzero"`
	FinishedAt       *time.Time `json:"finished_at,omitzero"`
}

// MarshalJSON keeps the machine-readable schema stable: unavailable optional
// unit fields are represented as null rather than an ambiguous empty string.
func (j Job) MarshalJSON() ([]byte, error) {
	type wire Job
	var timer any
	if j.TimerUnit != "" {
		timer = j.TimerUnit
	}
	return json.Marshal(struct {
		wire
		TimerUnit any `json:"timer_unit"`
	}{wire: wire(j), TimerUnit: timer})
}

func UnitName(prefix string, id JobID) string  { return fmt.Sprintf("%s-run-%d.service", prefix, id) }
func TimerName(prefix string, id JobID) string { return fmt.Sprintf("%s-run-%d.timer", prefix, id) }
func ServiceName(prefix, name string) string {
	return prefix + "-svc-" + EscapeUnitName(name) + ".service"
}

// EscapeUnitName uses the same rules as systemd-escape.  Service names are a
// unit-name component, rather than a path, so UnitNameEscape is intentional.
func EscapeUnitName(s string) string {
	return unit.UnitNameEscape(s)
}

func ParseID(s string) (JobID, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("invalid job id %q", s)
	}
	return JobID(n), nil
}

func Normalize(active, sub, freezer, result string) State {
	if freezer == "frozen" {
		return Paused
	}
	switch active {
	case "activating":
		return Starting
	case "active":
		if sub == "exited" && result == "success" {
			return Success
		}
		return Running
	case "deactivating":
		return Stopping
	case "failed":
		return Failed
	case "inactive":
		if result == "success" {
			return Success
		}
		// A loaded inactive unit is not the same thing as an absent slot. Keep
		// unexpected/incomplete metadata visible instead of calling it free.
		return Unknown
	}
	return Unknown
}

func ParseSignal(s string) (syscall.Signal, error) {
	s = strings.ToUpper(strings.TrimPrefix(s, "SIG"))
	if n, err := strconv.Atoi(s); err == nil && n > 0 && n < 65 {
		return syscall.Signal(n), nil
	}
	names := map[string]syscall.Signal{
		"HUP": syscall.SIGHUP, "INT": syscall.SIGINT, "QUIT": syscall.SIGQUIT,
		"ILL": syscall.SIGILL, "ABRT": syscall.SIGABRT, "FPE": syscall.SIGFPE,
		"KILL": syscall.SIGKILL, "SEGV": syscall.SIGSEGV, "PIPE": syscall.SIGPIPE,
		"ALRM": syscall.SIGALRM, "TERM": syscall.SIGTERM, "USR1": syscall.SIGUSR1,
		"USR2": syscall.SIGUSR2, "CHLD": syscall.SIGCHLD, "CONT": syscall.SIGCONT,
		"STOP": syscall.SIGSTOP, "TSTP": syscall.SIGTSTP, "TTIN": syscall.SIGTTIN,
		"TTOU": syscall.SIGTTOU,
	}
	if sig, ok := names[s]; ok {
		return sig, nil
	}
	return 0, fmt.Errorf("unknown signal %q", s)
}

// ParseDuration accepts Go durations plus the convenient day suffix used by
// the CLI. Negative and zero durations are not useful for delayed jobs.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		days, err := strconv.ParseInt(strings.TrimSuffix(s, "d"), 10, 64)
		if err != nil || days <= 0 || days > int64((2562047*time.Hour)/(24*time.Hour)) {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return d, nil
}

func TruncateLabel(label string, max int) string {
	if max <= 0 || len([]rune(label)) <= max {
		return label
	}
	return string([]rune(label)[:max-1]) + "…"
}

func BuildRunArgv(shell, script string) []string { return []string{shell, "-c", script} }
func BuildExecArgv(argv []string) []string       { return append([]string(nil), argv...) }
func BuildScript(script, local string, source bool) string {
	if source && local != "" {
		// Nu accepts doubled single quotes inside a single-quoted string.
		return "source '" + strings.ReplaceAll(local, "'", "''") + "'\n" + script
	}
	return script
}
func WrapDirenv(dir string, argv []string) []string {
	out := append([]string{"direnv", "exec", dir}, argv...)
	return out
}
func ResolveDir(path string) (string, error) {
	p, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return "", fmt.Errorf("%s is not a directory", path)
	}
	return p, nil
}
func Executable(name string) bool { _, err := exec.LookPath(name); return err == nil }
