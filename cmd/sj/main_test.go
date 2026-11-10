package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"sj/internal/core"
)

func TestLoadConfigAppliesEnvironmentWithoutConfigFile(t *testing.T) {
	t.Setenv("SJ_CONFIG", filepath.Join(t.TempDir(), "missing.toml"))
	t.Setenv("SJ_MAX_JOBS", "3")
	t.Setenv("SJ_SHELL", "bash")
	t.Setenv("SJ_DIRENV", "never")
	app := &app{max: 8, shell: "nu", direnv: "auto", prefix: "sj", logLines: 50, color: "auto"}
	if err := app.loadConfig(); err != nil {
		t.Fatal(err)
	}
	if app.max != 3 || app.shell != "bash" || app.direnv != "never" {
		t.Fatalf("config = max %d shell %q direnv %q", app.max, app.shell, app.direnv)
	}
}

func TestLoadConfigEnvironmentOverridesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("max_jobs = 2\nshell = \"nu\"\nunit_prefix = \"test\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SJ_CONFIG", path)
	t.Setenv("SJ_MAX_JOBS", "4")
	app := &app{max: 8, shell: "bash", direnv: "auto", prefix: "sj", logLines: 50, color: "auto"}
	if err := app.loadConfig(); err != nil {
		t.Fatal(err)
	}
	if app.max != 4 || app.shell != "nu" || app.prefix != "test" {
		t.Fatalf("config = max %d shell %q prefix %q", app.max, app.shell, app.prefix)
	}
}

func TestLoadConfigExtendedFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	contents := "source_local_nu = false\nlocal_nu_filename = \"project.nu\"\nmulti_log_lines = 7\nservice_restart = \"always\"\nservice_restart_sec = \"3s\"\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SJ_CONFIG", path)
	a := &app{max: 8, shell: "nu", direnv: "auto", local: true, localFile: "local.nu", prefix: "sj", logLines: 50, color: "auto"}
	if err := a.loadConfig(); err != nil {
		t.Fatal(err)
	}
	if a.local || a.localFile != "project.nu" || a.multiLogLines != 7 || a.serviceRestart != "always" || a.serviceRestartSec.Seconds() != 3 {
		t.Fatalf("unexpected config: %+v", a)
	}
}

func TestGlobalColorOverridesConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("color = \"always\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SJ_CONFIG", path)
	a := &app{max: 8, shell: "nu", direnv: "auto", local: true, localFile: "local.nu", prefix: "sj", logLines: 50, color: "auto"}
	root := a.rootCommand()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"--color", "never", "completions", "bash"})
	if err := root.Execute(); err != nil || a.color != "never" {
		t.Fatalf("color=%q err=%v", a.color, err)
	}
}

func TestLoadConfigAllowsCommentsAndSingleQuotedScalars(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("shell = 'bash' # shell\nmax_jobs = 2 # slots\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SJ_CONFIG", path)
	a := &app{max: 8, shell: "nu", direnv: "auto", prefix: "sj", logLines: 50, color: "auto"}
	if err := a.loadConfig(); err != nil {
		t.Fatal(err)
	}
	if a.shell != "bash" || a.max != 2 {
		t.Fatalf("config = shell %q max %d", a.shell, a.max)
	}
}

func TestDurationFlagSupportsDays(t *testing.T) {
	var d durationFlag
	if err := d.Set("1d"); err != nil || d.value != 24*time.Hour {
		t.Fatalf("duration = %v, %v", d.value, err)
	}
}

func TestParseAtTimeAcceptsRFC3339AndCLIForm(t *testing.T) {
	if got, err := parseAtTime("2026-09-08T02:00:00Z"); err != nil || got.Location() != time.UTC {
		t.Fatalf("RFC3339 time = %v, %v", got, err)
	}
	if _, err := parseAtTime("2026-09-08 02:00"); err != nil {
		t.Fatalf("CLI date-time was rejected: %v", err)
	}
	if _, err := parseAtTime("tomorrow"); err == nil {
		t.Fatal("invalid date-time was accepted")
	}
}

func TestReorderFlagsAllowsOptionsAfterOperands(t *testing.T) {
	got := reorderFlags([]string{"3", "--signal", "SIGKILL", "4"}, "signal")
	want := []string{"--signal", "SIGKILL", "3", "4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reordered = %q, want %q", got, want)
	}
}

func TestSystemdEnvironmentDropsInvalidNames(t *testing.T) {
	input := []string{
		"PATH=/usr/bin",
		"docker-cli=/tmp/docker",
		"_VALID=value=with=equals",
		"9INVALID=value",
		"MISSING_EQUALS",
		"ÜNICODE=value",
		"EMPTY=",
	}
	want := []string{"PATH=/usr/bin", "_VALID=value=with=equals", "EMPTY="}
	if got := systemdEnvironment(input); !reflect.DeepEqual(got, want) {
		t.Fatalf("systemdEnvironment() = %q, want %q", got, want)
	}
}

func TestJobPropertiesRetainCompletedJobsByDefault(t *testing.T) {
	properties := new(app).jobProperties([]string{"true"}, "/tmp", "test", true)
	for _, property := range properties {
		if property.Name == "RemainAfterExit" {
			if retained, ok := property.Value.Value().(bool); !ok || !retained {
				t.Fatalf("RemainAfterExit = %#v", property.Value.Value())
			}
			return
		}
	}
	t.Fatal("RemainAfterExit property is missing")
}

func TestJobPropertiesCollectsSuccessfulJobsWhenRequested(t *testing.T) {
	properties := new(app).jobProperties([]string{"true"}, "/tmp", "test", false)
	for _, property := range properties {
		if property.Name == "RemainAfterExit" {
			t.Fatal("auto-collected job unexpectedly retained")
		}
	}
}

func TestJobPropertiesDoNotIgnoreCommandFailures(t *testing.T) {
	properties := new(app).jobProperties([]string{"false"}, "/tmp", "test", true)
	for _, property := range properties {
		if property.Name != "ExecStart" {
			continue
		}
		commands := reflect.ValueOf(property.Value.Value())
		if commands.Len() != 1 {
			t.Fatalf("ExecStart has %d commands", commands.Len())
		}
		// go-systemd's historical field name is misleading: systemd interprets
		// this D-Bus boolean as ignore_errors.
		if commands.Index(0).FieldByName("UncleanIsFailure").Bool() {
			t.Fatal("ExecStart unexpectedly ignores command failures")
		}
		return
	}
	t.Fatal("ExecStart property is missing")
}

func TestNoteArgvKeepsUntimedNotesActive(t *testing.T) {
	if got, want := noteArgv(0), []string{"/bin/tail", "-f", "/dev/null"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("noteArgv(0) = %q, want %q", got, want)
	}
	if got, want := noteArgv(5*time.Minute), []string{"/bin/sleep", "5m0s"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("noteArgv(5m) = %q, want %q", got, want)
	}
}

func TestShowJobContentRemovesNoteMarker(t *testing.T) {
	if got, want := showJobContent("📓aaaaa"), "aaaaa"; got != want {
		t.Fatalf("showJobContent(note) = %q, want %q", got, want)
	}
	if got, want := showJobContent("Build tests"), "Build tests"; got != want {
		t.Fatalf("showJobContent(job) = %q, want %q", got, want)
	}
}

func TestIsUnitExists(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "dbus error value",
			err:  dbus.Error{Name: "org.freedesktop.systemd1.UnitExists", Body: []any{"Unit sj-run-1.service was already loaded or has a fragment file."}},
			want: true,
		},
		{
			name: "dbus error pointer",
			err:  &dbus.Error{Name: "org.freedesktop.systemd1.UnitExists", Body: []any{"Unit sj-run-1.service was already loaded or has a fragment file."}},
			want: true,
		},
		{
			name: "systemd already loaded text",
			err:  errors.New("Unit sj-run-1.service was already loaded or has a fragment file."),
			want: true,
		},
		{
			name: "already exists text",
			err:  errors.New("unit already exists"),
			want: true,
		},
		{
			name: "contains UnitExists name",
			err:  errors.New("org.freedesktop.systemd1.UnitExists"),
			want: true,
		},
		{
			name: "other dbus error",
			err:  dbus.Error{Name: "org.freedesktop.systemd1.NoSuchUnit"},
			want: false,
		},
		{
			name: "other error",
			err:  errors.New("permission denied"),
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUnitExists(tc.err); got != tc.want {
				t.Errorf("isUnitExists(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestShouldCleanJob(t *testing.T) {
	normalRunning := core.Job{ID: 1, Description: "build", State: core.Running}
	normalSuccess := core.Job{ID: 2, Description: "test", State: core.Success}
	normalFailed := core.Job{ID: 3, Description: "lint", State: core.Failed}
	normalScheduled := core.Job{ID: 4, Description: "deploy", State: core.Scheduled}

	noteRunning := core.Job{ID: 5, Description: "📓my note", State: core.Running}
	noteSuccess := core.Job{ID: 6, Description: "📓timed note", State: core.Success}
	noteFailed := core.Job{ID: 7, Description: "📓broken note", State: core.Failed}
	noteScheduled := core.Job{ID: 8, Description: "📓delayed note", State: core.Scheduled}

	// 1. Default clean (kind="clean", failedOnly=false, all=false)
	// Normal success/failed cleaned; running/scheduled skipped; ALL notes skipped!
	if shouldCleanJob("clean", normalRunning, false, false) {
		t.Error("normal running should not be cleaned")
	}
	if !shouldCleanJob("clean", normalSuccess, false, false) {
		t.Error("normal success should be cleaned")
	}
	if !shouldCleanJob("clean", normalFailed, false, false) {
		t.Error("normal failed should be cleaned")
	}
	if shouldCleanJob("clean", normalScheduled, false, false) {
		t.Error("normal scheduled should not be cleaned")
	}
	if shouldCleanJob("clean", noteRunning, false, false) {
		t.Error("note running should not be cleaned by default")
	}
	if shouldCleanJob("clean", noteSuccess, false, false) {
		t.Error("note success should not be cleaned by default")
	}
	if shouldCleanJob("clean", noteFailed, false, false) {
		t.Error("note failed should not be cleaned by default")
	}
	if shouldCleanJob("clean", noteScheduled, false, false) {
		t.Error("note scheduled should not be cleaned by default")
	}

	// 2. clean --all (kind="clean", failedOnly=false, all=true)
	// Normal running/scheduled still skipped; normal success/failed cleaned; ALL notes cleaned!
	if shouldCleanJob("clean", normalRunning, false, true) {
		t.Error("normal running should still not be cleaned with --all")
	}
	if !shouldCleanJob("clean", normalSuccess, false, true) {
		t.Error("normal success should be cleaned with --all")
	}
	if !shouldCleanJob("clean", normalFailed, false, true) {
		t.Error("normal failed should be cleaned with --all")
	}
	if !shouldCleanJob("clean", noteRunning, false, true) {
		t.Error("note running should be cleaned with --all")
	}
	if !shouldCleanJob("clean", noteSuccess, false, true) {
		t.Error("note success should be cleaned with --all")
	}
	if !shouldCleanJob("clean", noteFailed, false, true) {
		t.Error("note failed should be cleaned with --all")
	}
	if !shouldCleanJob("clean", noteScheduled, false, true) {
		t.Error("note scheduled should be cleaned with --all")
	}

	// 3. clean --failed-only (all=false)
	if !shouldCleanJob("clean", normalFailed, true, false) {
		t.Error("normal failed should be cleaned with --failed-only")
	}
	if shouldCleanJob("clean", normalSuccess, true, false) {
		t.Error("normal success should not be cleaned with --failed-only")
	}
	if shouldCleanJob("clean", noteFailed, true, false) {
		t.Error("note failed should not be cleaned with --failed-only when all=false")
	}

	// 4. clean --failed-only --all (all=true)
	if !shouldCleanJob("clean", noteFailed, true, true) {
		t.Error("note failed should be cleaned with --failed-only --all")
	}
	if shouldCleanJob("clean", noteRunning, true, true) {
		t.Error("note running should not be cleaned with --failed-only --all")
	}

	// 5. reset (kind="reset")
	if !shouldCleanJob("reset", normalRunning, false, false) {
		t.Error("reset should clean normal running")
	}
	if !shouldCleanJob("reset", noteRunning, false, false) {
		t.Error("reset should clean note running")
	}
}
