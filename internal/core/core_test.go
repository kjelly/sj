package core

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestNamesAndEscaping(t *testing.T) {
	if got := ServiceName("sj", "dev-web"); got != `sj-svc-dev\x2dweb.service` {
		t.Fatal(got)
	}
	if got := UnitName("sj", 3); got != "sj-run-3.service" {
		t.Fatal(got)
	}
	if got := EscapeUnitName(".日本/語"); got != `\x2e\xe6\x97\xa5\xe6\x9c\xac-\xe8\xaa\x9e` {
		t.Fatal(got)
	}
}

func TestParseDurationAndTruncateLabel(t *testing.T) {
	got, err := ParseDuration("2d")
	if err != nil || got != 48*time.Hour {
		t.Fatalf("ParseDuration = %v, %v", got, err)
	}
	if _, err := ParseDuration("0m"); err == nil {
		t.Fatal("zero duration was accepted")
	}
	if got := TruncateLabel("abcdef", 4); got != "abc…" {
		t.Fatal(got)
	}
}
func TestCommandConstructionPreservesArgv(t *testing.T) {
	got := BuildExecArgv([]string{"rsync", "foo bar", "/tmp"})
	want := []string{"rsync", "foo bar", "/tmp"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q", got)
	}
}

func TestAllocateSlotOnlySkipsCollisions(t *testing.T) {
	var tried []JobID
	id, err := AllocateSlot(3, func(id JobID) error {
		tried = append(tried, id)
		if id < 3 {
			return ErrUnitExists
		}
		return nil
	})
	if err != nil || id != 3 {
		t.Fatalf("allocation = %d, %v", id, err)
	}
	if !reflect.DeepEqual(tried, []JobID{1, 2, 3}) {
		t.Fatalf("tried %v", tried)
	}
	want := errors.New("permission denied")
	_, err = AllocateSlot(3, func(JobID) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("non-collision error = %v", err)
	}
}

func TestParseIDStartsAtOne(t *testing.T) {
	if _, err := ParseID("0"); err == nil {
		t.Fatal("zero job ID was accepted")
	}
	if got, err := ParseID("1"); err != nil || got != 1 {
		t.Fatalf("ParseID(1) = %d, %v", got, err)
	}
}

func TestJobJSONUsesNullTimerWhenUnavailable(t *testing.T) {
	b, err := json.Marshal(Job{ID: 1, Unit: "sj-run-1.service", Kind: "job", State: Running})
	if err != nil || !strings.Contains(string(b), `"timer_unit":null`) {
		t.Fatalf("json = %s, err=%v", b, err)
	}
}

func TestBuildScriptQuotesLocalPath(t *testing.T) {
	got := BuildScript("echo ok", "/tmp/a b/local.nu", true)
	if got != "source '/tmp/a b/local.nu'\necho ok" {
		t.Fatalf("%q", got)
	}
}
func TestStateAndSignal(t *testing.T) {
	if Normalize("failed", "failed", "", "exit-code") != Failed {
		t.Fatal("state")
	}
	if Normalize("inactive", "dead", "", "") != Unknown {
		t.Fatal("loaded inactive unit must not be treated as a free slot")
	}
	if Normalize("active", "running", "frozen", "") != Paused {
		t.Fatal("freeze")
	}
	if Normalize("active", "exited", "", "success") != Success {
		t.Fatal("retained successful service must report success")
	}
	sig, _ := ParseSignal("SIGINT")
	if sig != syscall.SIGINT {
		t.Fatal(sig)
	}
	if sig, err := ParseSignal("USR1"); err != nil || sig != syscall.SIGUSR1 {
		t.Fatalf("USR1 = %v, %v", sig, err)
	}
}
