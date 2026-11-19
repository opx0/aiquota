package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"aiquota/internal/model"
)

type fakeSource struct {
	providers  []model.Provider
	refreshes  []string
	reads      int
	refreshErr error
}

func (s *fakeSource) Snapshot() []model.Provider {
	s.reads++
	return s.providers
}

func (s *fakeSource) Refresh(_ context.Context, provider string) error {
	s.refreshes = append(s.refreshes, provider)
	return s.refreshErr
}

func key(value string) tea.KeyMsg {
	switch value {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value)}
	}
}

func providerData(now time.Time) []model.Provider {
	primaryReset := now.Add(3*time.Hour + 30*time.Minute)
	weeklyReset := now.Add(48*time.Hour + 10*time.Minute)
	fiveHours := 26.0
	sevenDays := 64.0
	full := 100.0
	return []model.Provider{
		{ID: "claude", Name: "Claude Code", Status: "unavailable", Message: "Waiting for status-line data"},
		{ID: "antigravity", Name: "Antigravity", Status: "healthy", Plan: "Google AI Pro", ObservedAt: &now, Windows: []model.Window{
			{ID: "gemini-5h", BucketID: "gemini", Label: "Five Hour Limit Remaining", Model: "Gemini Models", DurationMinutes: intPtr(300), RemainingPercent: &full, ResetsAt: &primaryReset},
			{ID: "gemini-7d", BucketID: "gemini", Label: "Weekly Limit Remaining", Model: "Gemini Models", DurationMinutes: intPtr(10080), RemainingPercent: &full, ResetsAt: &weeklyReset},
		}},
		{ID: "codex", Name: "Codex", Status: "healthy", Plan: "plus", ObservedAt: &now, Windows: []model.Window{
			{ID: "primary", BucketID: "codex", Label: "primary", DurationMinutes: intPtr(300), RemainingPercent: &fiveHours, ResetsAt: &primaryReset},
			{ID: "secondary", BucketID: "codex", Label: "secondary", DurationMinutes: intPtr(10080), RemainingPercent: &sevenDays, ResetsAt: &weeklyReset},
		}},
	}
}

func intPtr(value int) *int { return &value }

func newTestModel(source *fakeSource, now time.Time) *appModel {
	options := Options{
		Load:             func() ([]model.Provider, error) { return source.Snapshot(), nil },
		Refresh:          source.Refresh,
		WarningThreshold: 20,
	}
	m := newModel(options, source.providers, now)
	m.width = 92
	m.height = 32
	return m
}

func TestOverviewOrdersProvidersAndPairsPercentWithReset(t *testing.T) {
	now := time.Date(2026, time.November, 9, 8, 0, 0, 0, time.Local)
	source := &fakeSource{providers: providerData(now)}
	view := newTestModel(source, now).View()
	codex := strings.Index(view, "CODEX")
	antigravity := strings.Index(view, "ANTIGRAVITY")
	claude := strings.Index(view, "CLAUDE CODE")
	if codex < 0 || antigravity < 0 || claude < 0 || codex >= antigravity || antigravity >= claude {
		t.Fatalf("providers are missing or out of order:\n%s", view)
	}
	for _, want := range []string{"26%", "64%", "in 3h 30m", "in 2d", "▰", "PLUS"} {
		if !strings.Contains(view, want) {
			t.Errorf("overview does not contain %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "------") {
		t.Fatalf("overview uses heavy separator rules:\n%s", view)
	}
}

func TestCountdownIsDurationOnly(t *testing.T) {
	if got := Countdown(2*time.Hour + 5*time.Minute); got != "2h 5m" {
		t.Fatalf("Countdown() = %q, want duration without a duplicate prefix", got)
	}
}

func TestQuitKeyStopsTheProgram(t *testing.T) {
	m := newTestModel(&fakeSource{}, time.Now())
	_, command := m.Update(key("q"))
	if command == nil {
		t.Fatal("quit key did not return a quit command")
	}
}

func TestInitialViewHasFallbackTerminalSize(t *testing.T) {
	now := time.Now()
	source := &fakeSource{providers: providerData(now)}
	m := newModel(Options{Load: func() ([]model.Provider, error) { return source.Snapshot(), nil }, Refresh: source.Refresh}, source.providers, now)
	if view := m.View(); !strings.Contains(view, "QUOTA") || !strings.Contains(view, "CODEX") {
		t.Fatalf("initial view is empty before terminal resize event:\n%s", view)
	}
}

func TestWideDashboardUsesProviderRailAndQuotaTiles(t *testing.T) {
	now := time.Date(2026, time.November, 9, 8, 0, 0, 0, time.Local)
	source := &fakeSource{providers: providerData(now)}
	m := newTestModel(source, now)
	m.width = 132
	view := m.View()
	for _, want := range []string{"AIQUOTA", "PROVIDERS", "CODEX", "ANTIGRAVITY", "CLAUDE CODE", "5H WINDOW", "7D WINDOW", "26%", "PLUS", "RESETS IN"} {
		if !strings.Contains(strings.ToUpper(view), want) {
			t.Errorf("wide dashboard does not contain %q:\n%s", want, view)
		}
	}
	for _, line := range strings.Split(view, "\n") {
		if width := lipgloss.Width(line); width > m.width {
			t.Errorf("line is %d cells wide in %d-cell terminal: %q", width, m.width, line)
		}
	}
}

func TestDetailAndHelpNavigation(t *testing.T) {
	now := time.Date(2026, time.November, 9, 8, 0, 0, 0, time.Local)
	source := &fakeSource{providers: providerData(now)}
	m := newTestModel(source, now)
	m.Update(key("down"))
	m.Update(key("enter"))
	view := m.View()
	if !strings.Contains(view, "GEMINI MODELS") || !strings.Contains(view, "Reset") || !strings.Contains(view, "GOOGLE AI PRO") {
		t.Fatalf("provider detail is incomplete:\n%s", view)
	}
	m.Update(key("?"))
	if !strings.Contains(m.View(), "KEYBOARD") || !strings.Contains(strings.ToLower(m.View()), "refresh") {
		t.Fatalf("help screen is missing key bindings:\n%s", m.View())
	}
	m.Update(key("esc"))
	if !strings.Contains(m.View(), "CODEX") || !strings.Contains(m.View(), "ANTIGRAVITY") {
		t.Fatalf("escape did not return to overview:\n%s", m.View())
	}
}

func TestRefreshRunsAsCommandAndReportsErrors(t *testing.T) {
	now := time.Now()
	source := &fakeSource{providers: providerData(now), refreshErr: errors.New("codex refresh failed")}
	m := newTestModel(source, now)
	next, cmd := m.Update(key("R"))
	if cmd == nil || len(source.refreshes) != 0 {
		t.Fatal("refresh blocked Update or ran synchronously")
	}
	msg := cmd()
	next, _ = next.Update(msg)
	m = next.(*appModel)
	if len(source.refreshes) != 1 || source.refreshes[0] != "" {
		t.Fatalf("refresh all called with %v", source.refreshes)
	}
	if !strings.Contains(m.View(), "codex refresh failed") {
		t.Fatalf("refresh error is not visible:\n%s", m.View())
	}
	if m.refreshing {
		t.Fatal("refresh state remained active after completion")
	}
}

func TestRefreshTickReloadsOnlyCachedSnapshot(t *testing.T) {
	now := time.Now()
	source := &fakeSource{providers: providerData(now)}
	m := newTestModel(source, now)
	reads := source.reads
	m.Update(cacheTickMsg{now: now.Add(5 * time.Second)})
	if source.reads != reads+1 {
		t.Fatalf("cache tick read count = %d, want %d", source.reads, reads+1)
	}
	if len(source.refreshes) != 0 {
		t.Fatalf("cache tick started provider refresh: %v", source.refreshes)
	}
}

func TestUnknownQuotaStaysUnknownAndNarrowViewFits(t *testing.T) {
	now := time.Now()
	p := model.Provider{ID: "codex", Name: "Codex", Status: "healthy", Windows: []model.Window{{ID: "primary", Label: "Primary", DurationMinutes: intPtr(300)}}}
	source := &fakeSource{providers: []model.Provider{p}}
	m := newModel(Options{Load: func() ([]model.Provider, error) { return source.Snapshot(), nil }, Refresh: source.Refresh}, source.providers, now)
	m.width = 36
	m.height = 16
	view := m.View()
	if !strings.Contains(view, "N/A") || strings.Contains(view, "0%") || strings.Contains(view, "▰") {
		t.Fatalf("unknown quota was shown as a measured value:\n%s", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if width := lipgloss.Width(line); width > m.width {
			t.Errorf("line is %d cells wide in %d-cell terminal: %q", width, m.width, line)
		}
	}
}

func TestTickScrollsLongDetail(t *testing.T) {
	now := time.Now()
	p := model.Provider{ID: "antigravity", Name: "Antigravity", Status: "healthy"}
	for i := range 12 {
		p.Windows = append(p.Windows, model.Window{ID: "window", Label: "Quota window", DurationMinutes: intPtr((i + 1) * 60)})
	}
	source := &fakeSource{providers: []model.Provider{p}}
	m := newModel(Options{Load: func() ([]model.Provider, error) { return source.Snapshot(), nil }, Refresh: source.Refresh}, source.providers, now)
	m.width = 52
	m.height = 10
	m.Update(key("enter"))
	first := m.View()
	m.Update(key("down"))
	second := m.View()
	if first == second {
		t.Fatalf("detail view did not scroll:\n%s", first)
	}
}
