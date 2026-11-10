package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	systemd "github.com/coreos/go-systemd/v22/dbus"
	"github.com/godbus/dbus/v5"
	"sj/internal/core"
)

type startRequest struct {
	Name       string
	Mode       string
	Properties []systemd.Property
	Aux        []systemd.PropertyCollection
}

type fakeStartManager struct {
	bus       *dbus.Conn
	requests  chan startRequest
	result    string
	collision atomic.Bool
}

func (m *fakeStartManager) Subscribe() *dbus.Error { return nil }
func (m *fakeStartManager) StartTransientUnit(name, mode string, properties []systemd.Property, aux []systemd.PropertyCollection) (dbus.ObjectPath, *dbus.Error) {
	m.requests <- startRequest{name, mode, properties, aux}
	if m.collision.Load() && strings.HasSuffix(name, "-1.service") {
		return "", &dbus.Error{Name: "org.freedesktop.systemd1.UnitExists"}
	}
	job := dbus.ObjectPath("/org/freedesktop/systemd1/job/42")
	if m.result != "silent" {
		// Both signals precede the method reply; only the returned job may win.
		if err := m.bus.Emit(managerPath, managerInterface+".JobRemoved", uint32(41), dbus.ObjectPath("/org/freedesktop/systemd1/job/41"), name, "failed"); err != nil {
			return "", dbus.MakeFailedError(err)
		}
		if err := m.bus.Emit(managerPath, managerInterface+".JobRemoved", uint32(42), job, name, m.result); err != nil {
			return "", dbus.MakeFailedError(err)
		}
	}
	return job, nil
}

func privateManager(t *testing.T, result string) *fakeStartManager {
	t.Helper()
	path, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon unavailable")
	}
	// Keep Unix socket paths below sockaddr_un's limit, even for long test names.
	dir, err := os.MkdirTemp(os.TempDir(), "bus-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	cmd := exec.Command(path, "--session", "--nofork", "--print-address=1", "--nopidfile", "--address=unix:path="+filepath.Join(dir, "bus"))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	address, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", strings.TrimSpace(address))
	bus, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	manager := &fakeStartManager{bus: bus, requests: make(chan startRequest, 8), result: result}
	if err := bus.Export(manager, managerPath, managerInterface); err != nil {
		t.Fatal(err)
	}
	reply, err := bus.RequestName("org.freedesktop.systemd1", dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("RequestName = %v, %v", reply, err)
	}
	return manager
}

func propertyValue(properties []systemd.Property, name string) any {
	for _, property := range properties {
		if property.Name == name {
			return property.Value.Value()
		}
	}
	return nil
}

func TestTimerProperties(t *testing.T) {
	properties, err := timerProperties("delayed", time.Second, "")
	if err != nil {
		t.Fatal(err)
	}
	monotonic := propertyValue(properties, "TimersMonotonic")
	if signature := dbus.SignatureOf(monotonic).String(); signature != "a(st)" {
		t.Fatal(signature)
	}
	entry := reflect.ValueOf(monotonic).Index(0)
	if entry.Field(0).String() != "OnActiveSec" || entry.Field(1).Uint() != 1000000 {
		t.Fatal(monotonic)
	}
	if propertyValue(properties, "RemainAfterElapse") != false {
		t.Fatal("timer would retain collected slots")
	}
	for _, input := range []string{"2099-01-01T02:00:00Z", "2099-01-01T10:00:00+08:00"} {
		properties, err := timerProperties("scheduled", 0, input)
		if err != nil {
			t.Fatal(err)
		}
		calendar := propertyValue(properties, "TimersCalendar")
		if signature := dbus.SignatureOf(calendar).String(); signature != "a(ss)" {
			t.Fatal(signature)
		}
		entry := reflect.ValueOf(calendar).Index(0)
		spec := entry.Field(1).String()
		if spec != "2099-01-01 02:00:00 UTC" {
			t.Fatal(spec)
		}
		if path, err := exec.LookPath("systemd-analyze"); err == nil {
			if output, err := exec.Command(path, "calendar", spec).CombinedOutput(); err != nil {
				t.Fatalf("%v: %s", err, output)
			}
		}
	}
	if _, err := timerProperties("bad", 0, "invalid"); err == nil {
		t.Fatal("invalid calendar accepted")
	}
}

func TestMergeTimersPreservesServiceState(t *testing.T) {
	for _, state := range []core.State{core.Running, core.Paused, core.Success, core.Failed} {
		for _, sub := range []string{"running", "elapsed"} {
			t.Run(fmt.Sprintf("%s/%s", state, sub), func(t *testing.T) {
				jobs := []core.Job{{ID: 1, Unit: "test-run-1.service", State: state}}
				timers := []systemd.UnitStatus{{Name: "test-run-1.timer", ActiveState: "active", SubState: sub}}
				got := mergeTimers("test", jobs, map[core.JobID]int{1: 0}, timers)
				if got[0].State != state || got[0].TimerUnit != timers[0].Name {
					t.Fatal(got)
				}
				if state == core.Success || state == core.Failed {
					if !shouldCleanJob("clean", got[0], false, false) {
						t.Fatal("completed delayed job cannot be cleaned")
					}
				}
			})
		}
	}
	timers := []systemd.UnitStatus{{Name: "test-run-1.timer", ActiveState: "active", SubState: "waiting"}}
	got := mergeTimers("test", nil, nil, timers)
	if len(got) != 1 || got[0].State != core.Scheduled {
		t.Fatal(got)
	}
}

func TestStartTransientWaitsForMatchingResult(t *testing.T) {
	for _, result := range []string{"done", "failed", "canceled", "timeout", "dependency", "skipped"} {
		t.Run(result, func(t *testing.T) {
			manager := privateManager(t, result)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			err := startTransientUnitContext(ctx, "test-run-1.service", nil, nil)
			if result == "done" && err != nil {
				t.Fatal(err)
			}
			if result != "done" && (err == nil || !strings.Contains(err.Error(), result)) {
				t.Fatalf("result %s: %v", result, err)
			}
			request := <-manager.requests
			if request.Mode != "fail" {
				t.Fatal(request)
			}
		})
	}
}

func TestStartTransientTimeout(t *testing.T) {
	privateManager(t, "silent")
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	err := startTransientUnitContext(ctx, "test-run-1.service", nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestStartJobSkipsOnlyCollisions(t *testing.T) {
	manager := privateManager(t, "done")
	manager.collision.Store(true)
	a := newApp()
	a.prefix, a.max = "test", 2
	id, err := a.startJob([]string{"/bin/true"}, t.TempDir(), "test", 0, "", true)
	if err != nil || id != 2 {
		t.Fatalf("id=%d err=%v", id, err)
	}
	if first, second := <-manager.requests, <-manager.requests; first.Name != "test-run-1.service" || second.Name != "test-run-2.service" {
		t.Fatal(first, second)
	}
}

func TestStartDelayedCreatesTimerAndServiceTogether(t *testing.T) {
	manager := privateManager(t, "done")
	a := newApp()
	a.prefix = "test"
	if err := a.startDelayed(1, []string{"/bin/true"}, t.TempDir(), "test", time.Second, "", false); err != nil {
		t.Fatal(err)
	}
	request := <-manager.requests
	if request.Name != "test-run-1.timer" || len(request.Aux) != 1 || request.Aux[0].Name != "test-run-1.service" {
		t.Fatal(request)
	}
	if propertyValue(request.Properties, "TimersMonotonic") == nil || propertyValue(request.Properties, "Unit") != nil {
		t.Fatal(request)
	}
	if propertyValue(request.Aux[0].Properties, "RemainAfterExit") != nil {
		t.Fatal("collected service retained")
	}
}
