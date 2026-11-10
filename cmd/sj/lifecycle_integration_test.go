package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"sj/internal/core"
)

// Opt in because these tests create transient units in the real user manager.
// Every unit uses a unique namespace, and cleanup only touches that namespace.
func TestSystemdLifecycle(t *testing.T) {
	if os.Getenv("SJ_SYSTEMD_TEST") != "1" {
		t.Skip("set SJ_SYSTEMD_TEST=1 to test the real systemd user manager")
	}
	// t.Context is canceled before Cleanup runs; this connection must remain
	// usable until our transient units have been stopped and unloaded.
	conn, err := systemdUserConnection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Close)
	a := newApp()
	a.conn = conn
	a.prefix = fmt.Sprintf("sjtest-%d-%d", os.Getpid(), time.Now().UnixNano())
	t.Logf("test unit prefix: %s", a.prefix)
	a.max = 2
	dir := t.TempDir()
	cleanup := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for id := core.JobID(1); id <= 2; id++ {
			for _, unit := range []string{core.TimerName(a.prefix, id), core.UnitName(a.prefix, id)} {
				_, _ = conn.StopUnitContext(ctx, unit, "replace", nil)
				_ = conn.ResetFailedUnitContext(ctx, unit)
			}
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			jobs, err := a.jobs()
			if err == nil && len(jobs) == 0 {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Error("test units did not unload after cleanup")
	}
	t.Cleanup(cleanup)

	await := func(id core.JobID, want core.State) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		var last []core.Job
		for time.Now().Before(deadline) {
			jobs, err := a.jobs()
			if err != nil {
				t.Fatal(err)
			}
			last = jobs
			for _, job := range jobs {
				if job.ID == id && job.State == want {
					return
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("job %d never reached %s: %+v", id, want, last)
	}

	for _, schedule := range []string{"immediate", "delay", "at"} {
		t.Run(schedule, func(t *testing.T) {
			var delay time.Duration
			var at string
			if schedule == "delay" {
				delay = 500 * time.Millisecond
			}
			if schedule == "at" {
				at = time.Now().Add(500 * time.Millisecond).Format(time.RFC3339Nano)
			}
			id, err := a.startJob([]string{"/bin/sleep", "1"}, dir, schedule, delay, at, true)
			if err != nil {
				t.Fatal(err)
			}
			if schedule != "immediate" {
				await(id, core.Scheduled)
			}
			await(id, core.Running)
			await(id, core.Success)
			if code := a.wait([]string{fmt.Sprint(id)}); code != 0 {
				t.Fatalf("wait exit = %d", code)
			}
			if code := a.clean("clean", nil); code != 0 {
				t.Fatalf("clean exit = %d", code)
			}
			cleanup()
		})
	}

	t.Run("failed delayed task", func(t *testing.T) {
		id, err := a.startJob([]string{"/bin/sh", "-c", "exit 7"}, dir, "failure", 50*time.Millisecond, "", true)
		if err != nil {
			t.Fatal(err)
		}
		await(id, core.Failed)
		if code := a.wait([]string{fmt.Sprint(id)}); code != 1 {
			t.Fatalf("wait exit = %d", code)
		}
		cleanup()
	})

	t.Run("missing executable", func(t *testing.T) {
		_, err := a.startJob([]string{dir + "/missing-executable"}, dir, "missing", 0, "", true)
		if err == nil || !strings.Contains(err.Error(), "failed") {
			t.Fatalf("missing executable startup = %v", err)
		}
		cleanup()
	})

	t.Run("collect releases timer and slot", func(t *testing.T) {
		if _, err := a.startJob([]string{"/bin/true"}, dir, "collect", 50*time.Millisecond, "", false); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			jobs, err := a.jobs()
			if err != nil {
				t.Fatal(err)
			}
			if len(jobs) == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("collected units remain: %+v", jobs)
			}
			time.Sleep(20 * time.Millisecond)
		}
		id, err := a.startJob([]string{"/bin/true"}, dir, "reuse", 50*time.Millisecond, "", true)
		if err != nil || id != 1 {
			t.Fatalf("reused slot = %d, %v", id, err)
		}
		await(id, core.Success)
		cleanup()
	})

	t.Run("concurrent allocation", func(t *testing.T) {
		ids := make(chan core.JobID, 2)
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for range 2 {
			wg.Go(func() {
				id, err := a.startJob([]string{"/bin/sleep", "1"}, dir, "concurrent", 0, "", true)
				ids <- id
				errs <- err
			})
		}
		wg.Wait()
		for range 2 {
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
		}
		if first, second := <-ids, <-ids; first == second || first == 0 || second == 0 {
			t.Fatalf("concurrent IDs = %d, %d", first, second)
		}
		cleanup()
	})
}
