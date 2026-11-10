package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"sj/internal/core"
)

type promptState struct {
	state  core.State
	symbol string
}

const promptSlotDigits = "①②③④⑤⑥⑦⑧⑨"

const tmuxPromptDetailedLimit = 45

var promptStates = [...]promptState{
	{core.Starting, "…"},
	{core.Running, "▶"},
	{core.Scheduled, "◷"},
	{core.Paused, "Ⅱ"},
	{core.Failed, "❌"},
	{core.Success, "✅"},
	{core.Stopping, "■"},
	{core.Unknown, "?"},
}

func (a *app) prompt(args []string) int {
	jobs, err := a.jobs()
	if err != nil {
		if contains(args, "--strict") {
			return fail(3, err)
		}
		return 0
	}
	if contains(args, "--json") {
		return printJSON(map[string]any{"schema_version": 1, "jobs": jobs})
	}
	if contains(args, "--tmux") {
		fmt.Println(renderTmuxPrompt(jobs, a.max, time.Now()))
		return 0
	}
	for i, job := range jobs {
		if i > 0 {
			fmt.Print(" ")
		}
		icon := map[core.State]string{core.Running: "🟢", core.Starting: "🟢", core.Scheduled: "⏰", core.Failed: "❌", core.Paused: "⏸"}[job.State]
		fmt.Printf("[%d:%s%s]", job.ID, icon, job.Description)
	}
	fmt.Println()
	return 0
}

func renderTmuxPrompt(jobs []core.Job, maxJobs int, now time.Time) string {
	counts := make(map[core.State]int, len(promptStates))
	for _, job := range jobs {
		counts[job.State]++
	}

	var summary strings.Builder
	summary.WriteString("sj(")
	if len(jobs) == 0 {
		fmt.Fprintf(&summary, "0/%d)", maxJobs)
		return summary.String()
	}
	for _, item := range promptStates {
		if count := counts[item.state]; count > 0 {
			fmt.Fprintf(&summary, "%s%d", promptSummarySymbol(item.state), count)
		}
	}
	fmt.Fprintf(&summary, "/%d)", maxJobs)

	parts := make([]string, 0, len(jobs)+1)
	parts = append(parts, summary.String())
	for _, job := range jobs {
		parts = append(parts, renderTmuxJob(job, now))
	}
	detailed := strings.Join(parts, "│")
	if promptDisplayWidth(detailed) <= tmuxPromptDetailedLimit {
		return detailed
	}
	return renderCompactTmuxPrompt(jobs)
}

// promptDisplayWidth approximates tmux's terminal-column width. Rune count is
// not sufficient here because emoji and East Asian characters commonly occupy
// two columns, while combining marks and zero-width joiners occupy none.
func promptDisplayWidth(text string) int {
	width := 0
	for _, r := range text {
		switch {
		case unicode.IsControl(r), unicode.Is(unicode.Mn, r), unicode.Is(unicode.Me, r), unicode.Is(unicode.Cf, r):
		case promptWideRune(r):
			width += 2
		default:
			width++
		}
	}
	return width
}

func promptWideRune(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115f,
		r >= 0x2329 && r <= 0x232a,
		r >= 0x2600 && r <= 0x27bf,
		r >= 0x2e80 && r <= 0xa4cf,
		r >= 0xac00 && r <= 0xd7a3,
		r >= 0xf900 && r <= 0xfaff,
		r >= 0xfe10 && r <= 0xfe6f,
		r >= 0xff01 && r <= 0xff60,
		r >= 0xffe0 && r <= 0xffe6,
		r >= 0x1f000 && r <= 0x1faff,
		r >= 0x20000 && r <= 0x3fffd:
		return true
	default:
		return false
	}
}

func renderCompactTmuxPrompt(jobs []core.Job) string {
	parts := make([]string, 0, len(jobs)+1)
	parts = append(parts, "sj")
	for _, job := range jobs {
		parts = append(parts, promptSlotSymbol(job.ID)+promptCompactStateSymbol(job.State))
	}
	return strings.Join(parts, "│")
}

func renderTmuxJob(job core.Job, now time.Time) string {
	var fragment strings.Builder
	label := sanitizeTmuxText(job.Description)
	if label == "" {
		label = "-"
	}
	fmt.Fprintf(&fragment, "%s%s%s", promptSlotSymbol(job.ID), promptStateSymbol(job.State), label)

	details := make([]string, 0, 5)
	switch job.State {
	case core.Starting, core.Running, core.Paused, core.Stopping:
		if job.StartedAt != nil {
			details = append(details, formatPromptDuration(now.Sub(*job.StartedAt)))
		}
	case core.Scheduled:
	case core.Unknown:
		rawState := sanitizeTmuxText(strings.Trim(job.ActiveState+"/"+job.SubState, "/"))
		if rawState != "" {
			details = append(details, rawState)
		}
	}
	if job.MainPID != 0 {
		details = append(details, fmt.Sprintf("p%d", job.MainPID))
	}
	if job.WorkingDirectory != "" {
		details = append(details, "@"+sanitizeTmuxText(filepath.Base(job.WorkingDirectory)))
	}
	if len(details) > 0 {
		fragment.WriteString("·")
		fragment.WriteString(strings.Join(details, "·"))
	}
	return fragment.String()
}

func promptStateSymbol(state core.State) string {
	for _, item := range promptStates {
		if item.state == state {
			return item.symbol
		}
	}
	return "?"
}

func promptSlotSymbol(id core.JobID) string {
	if id >= 1 && id < 10 {
		return string([]rune(promptSlotDigits)[int(id)-1])
	}
	if id >= 10 && id <= 20 {
		return string(rune('⑩' + rune(id-10)))
	}
	return fmt.Sprintf("%d", id)
}

func promptSummarySymbol(state core.State) string {
	switch state {
	case core.Failed:
		return "🔴"
	case core.Success:
		return "🟢"
	default:
		return promptStateSymbol(state)
	}
}

func promptCompactStateSymbol(state core.State) string {
	switch state {
	case core.Success:
		return "✅"
	case core.Failed:
		return "❌"
	case core.Scheduled:
		return "⏰"
	case core.Paused:
		return "⏸"
	case core.Stopping:
		return "⏹"
	case core.Starting, core.Running:
		return "⏳"
	default:
		return "❔"
	}
}

func formatPromptDuration(duration time.Duration) string {
	duration = max(duration, 0) / time.Second * time.Second
	days := duration / (24 * time.Hour)
	duration %= 24 * time.Hour
	hours := duration / time.Hour
	duration %= time.Hour
	minutes := duration / time.Minute
	seconds := duration % time.Minute / time.Second
	switch {
	case days > 0:
		return fmt.Sprintf("%dd%dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh%dm", hours, minutes)
	case minutes > 0:
		return fmt.Sprintf("%dm%ds", minutes, seconds)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

func sanitizeTmuxText(text string) string {
	text = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r):
			return ' '
		case r == '#':
			// Prevent labels from injecting tmux formats or styles.
			return '＃'
		case r == '│':
			return '¦'
		default:
			return r
		}
	}, text)
	return strings.Join(strings.Fields(text), " ")
}
