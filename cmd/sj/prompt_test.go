package main

import (
	"strings"
	"testing"
	"time"

	"sj/internal/core"
)

func TestRenderTmuxPromptIdle(t *testing.T) {
	if got, want := renderTmuxPrompt(nil, 8, time.Time{}), "sj(0/8)"; got != want {
		t.Fatalf("renderTmuxPrompt() = %q, want %q", got, want)
	}
}

func TestRenderTmuxPromptCompactsAndRenderJobKeepsDetails(t *testing.T) {
	now := time.Date(2026, time.September, 19, 12, 0, 0, 0, time.UTC)
	jobs := []core.Job{
		{
			ID:               1,
			State:            core.Running,
			Description:      "build",
			MainPID:          1234,
			StartedAt:        new(now.Add(-5*time.Minute - 12*time.Second)),
			WorkingDirectory: "/work/project",
		},
		{ID: 2, State: core.Scheduled, Description: "backup"},
		{
			ID:               3,
			State:            core.Failed,
			Description:      "tests",
			Result:           "exit-code",
			ExecMainStatus:   1,
			WorkingDirectory: "/work/project",
		},
	}
	if got, want := renderTmuxJob(jobs[0], now), "①▶build·5m12s·p1234·@project"; got != want {
		t.Fatalf("renderTmuxJob() = %q, want %q", got, want)
	}
	want := "sj│①⏳│②⏰│③❌"
	if got := renderTmuxPrompt(jobs, 8, now); got != want {
		t.Fatalf("renderTmuxPrompt() =\n%q\nwant\n%q", got, want)
	}
}

func TestRenderTmuxPromptCompactsWhenDetailedOutputIsTooLong(t *testing.T) {
	jobs := make([]core.Job, 8)
	for i := range jobs {
		jobs[i] = core.Job{ID: core.JobID(i + 1), State: core.Running, Description: "a very long job label"}
	}
	want := "sj│①⏳│②⏳│③⏳│④⏳│⑤⏳│⑥⏳│⑦⏳│⑧⏳"
	if got := renderTmuxPrompt(jobs, 8, time.Time{}); got != want {
		t.Fatalf("renderTmuxPrompt() = %q, want %q", got, want)
	}
}

func TestRenderTmuxPromptUsesDisplayWidthForEmoji(t *testing.T) {
	jobs := make([]core.Job, 4)
	for i := range jobs {
		jobs[i] = core.Job{
			ID:               core.JobID(i + 1),
			State:            core.Success,
			Description:      "ps",
			WorkingDirectory: "/tmp/sj",
		}
	}
	want := "sj│①✅│②✅│③✅│④✅"
	if got := renderTmuxPrompt(jobs, 8, time.Time{}); got != want {
		t.Fatalf("renderTmuxPrompt() = %q, want %q", got, want)
	}
}

func TestPromptDisplayWidthCountsEmojiAsTwoColumns(t *testing.T) {
	if got, want := promptDisplayWidth("sj(🟢3/8)"), 9; got != want {
		t.Fatalf("promptDisplayWidth() = %d, want %d", got, want)
	}
	if got, want := promptDisplayWidth("①✅"), 3; got != want {
		t.Fatalf("promptDisplayWidth() = %d, want %d", got, want)
	}
}

func TestRenderTmuxPromptSanitizesTmuxFormatsAndControlCharacters(t *testing.T) {
	jobs := []core.Job{{
		ID:          4,
		State:       core.Unknown,
		Description: "bad\n#[fg=red] │ label\x1b",
		ActiveState: "inactive",
		SubState:    "dead",
	}}
	got := renderTmuxJob(jobs[0], time.Time{})
	if strings.Contains(got, "#[") || strings.ContainsAny(got, "\n\r\x1b") {
		t.Fatalf("renderTmuxPrompt() contains unsafe control or tmux format: %q", got)
	}
	want := "④?bad ＃[fg=red] ¦ label·inactive/dead"
	if got != want {
		t.Fatalf("renderTmuxPrompt() = %q, want %q", got, want)
	}
}

func TestPromptSlotSymbolUsesUnicodeCircledDigits(t *testing.T) {
	tests := map[core.JobID]string{
		1:  "①",
		9:  "⑨",
		10: "⑩",
		20: "⑳",
		0:  "0",
		21: "21",
	}
	for id, want := range tests {
		if got := promptSlotSymbol(id); got != want {
			t.Errorf("promptSlotSymbol(%d) = %q, want %q", id, got, want)
		}
	}
}

func TestFormatPromptDuration(t *testing.T) {
	tests := map[time.Duration]string{
		-1 * time.Second:                      "0s",
		42*time.Second + 900*time.Millisecond: "42s",
		5*time.Minute + 12*time.Second:        "5m12s",
		3*time.Hour + 7*time.Minute:           "3h7m",
		2*24*time.Hour + 4*time.Hour:          "2d4h",
	}
	for duration, want := range tests {
		if got := formatPromptDuration(duration); got != want {
			t.Errorf("formatPromptDuration(%v) = %q, want %q", duration, got, want)
		}
	}
}
