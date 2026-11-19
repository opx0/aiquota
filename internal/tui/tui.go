package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/muesli/termenv"

	"aiquota/internal/model"
)

const cacheInterval = 5 * time.Second
const noticeDuration = 8 * time.Second

var providerOrder = map[string]int{"codex": 0, "antigravity": 1, "claude": 2}

type Options struct {
	Load             func() ([]model.Provider, error)
	Refresh          func(context.Context, string) error
	Color            bool
	WarningThreshold float64
	RefreshOnStart   bool
	Input            io.Reader
	Output           io.Writer
}

type screen uint8

const (
	overviewScreen screen = iota
	detailScreen
	helpScreen
)

type cacheTickMsg struct {
	now time.Time
}

type refreshDoneMsg struct {
	err error
}

type styles struct {
	title    lipgloss.Style
	primary  lipgloss.Style
	tertiary lipgloss.Style
	error    lipgloss.Style
	healthy  lipgloss.Style
	warning  lipgloss.Style
	muted    lipgloss.Style
	quiet    lipgloss.Style
	selected lipgloss.Style
	panel    lipgloss.Style
	focus    lipgloss.Style
}

type appModel struct {
	load        func() ([]model.Provider, error)
	refresh     func(context.Context, string) error
	ctx         context.Context
	options     Options
	styles      styles
	providers   []model.Provider
	selected    int
	page        screen
	width       int
	height      int
	scroll      int
	now         time.Time
	refreshing  bool
	notice      string
	noticeUntil time.Time
}

func Run(ctx context.Context, options Options) error {
	if options.Load == nil || options.Refresh == nil {
		return errors.New("TUI requires cache and refresh functions")
	}
	if options.Input == nil {
		options.Input = os.Stdin
	}
	if options.Output == nil {
		options.Output = os.Stdout
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	providers, loadErr := options.Load()
	m := newModel(options, providers, time.Now())
	if output, ok := options.Output.(*os.File); ok {
		if width, height, err := term.GetSize(output.Fd()); err == nil {
			m.width = width
			m.height = height
		}
	}
	if loadErr != nil {
		m.notice = "Cache read failed: " + loadErr.Error()
		m.noticeUntil = m.now.Add(noticeDuration)
	}
	m.ctx = runCtx
	programOptions := []tea.ProgramOption{tea.WithAltScreen(), tea.WithFPS(30), tea.WithContext(runCtx)}
	programOptions = append(programOptions, tea.WithInput(options.Input))
	if output, ok := options.Output.(*os.File); !ok || output != os.Stdout {
		programOptions = append(programOptions, tea.WithOutput(options.Output))
	}
	program := tea.NewProgram(m, programOptions...)
	if _, err := program.Run(); err != nil && !errors.Is(err, tea.ErrProgramKilled) && !errors.Is(err, tea.ErrInterrupted) {
		return fmt.Errorf("run TUI: %w", err)
	}
	return nil
}

func newModel(options Options, providers []model.Provider, now time.Time) *appModel {
	if options.WarningThreshold < 0 || options.WarningThreshold > 100 {
		options.WarningThreshold = 20
	}
	return &appModel{
		load:      options.Load,
		refresh:   options.Refresh,
		ctx:       context.Background(),
		options:   options,
		styles:    newStyles(options.Color, options.Output),
		providers: ordered(providers),
		width:     80,
		height:    24,
		now:       now,
	}
}

func newStyles(colorEnabled bool, output io.Writer) styles {
	if output == nil {
		output = os.Stdout
	}
	renderer := lipgloss.NewRenderer(output)
	if !colorEnabled {
		renderer.SetColorProfile(termenv.Ascii)
	}
	return styles{
		title:    renderer.NewStyle().Bold(true).Foreground(lipgloss.Color("#e7e2f3")),
		primary:  renderer.NewStyle().Foreground(lipgloss.Color("#c9c1ff")),
		tertiary: renderer.NewStyle().Foreground(lipgloss.Color("#edb7d7")),
		error:    renderer.NewStyle().Foreground(lipgloss.Color("#ffaaa8")),
		healthy:  renderer.NewStyle().Foreground(lipgloss.Color("#8ed9b1")),
		warning:  renderer.NewStyle().Foreground(lipgloss.Color("#edc27c")),
		muted:    renderer.NewStyle().Foreground(lipgloss.Color("#aaa5b4")),
		quiet:    renderer.NewStyle().Foreground(lipgloss.Color("#716d79")),
		selected: renderer.NewStyle().Bold(true).Foreground(lipgloss.Color("#c9c1ff")),
		panel:    renderer.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#37333f")).Padding(0, 1),
		focus:    renderer.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#7766b0")).Padding(0, 1),
	}
}

func ordered(providers []model.Provider) []model.Provider {
	result := append([]model.Provider(nil), providers...)
	sort.SliceStable(result, func(i, j int) bool {
		left, leftOK := providerOrder[result[i].ID]
		right, rightOK := providerOrder[result[j].ID]
		if !leftOK {
			left = len(providerOrder)
		}
		if !rightOK {
			right = len(providerOrder)
		}
		return left < right
	})
	return result
}

func (m *appModel) Init() tea.Cmd {
	commands := []tea.Cmd{cacheTick()}
	if m.options.RefreshOnStart {
		if command := m.startRefresh(""); command != nil {
			commands = append(commands, command)
		}
	}
	return tea.Batch(commands...)
}

func cacheTick() tea.Cmd {
	return tea.Tick(cacheInterval, func(now time.Time) tea.Msg {
		return cacheTickMsg{now: now}
	})
}

func (m *appModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.clampScroll()
		return m, nil
	case cacheTickMsg:
		m.now = msg.now
		m.loadSnapshot()
		m.clampSelection()
		if !m.noticeUntil.IsZero() && !m.now.Before(m.noticeUntil) {
			m.notice = ""
			m.noticeUntil = time.Time{}
		}
		m.clampScroll()
		return m, cacheTick()
	case refreshDoneMsg:
		m.refreshing = false
		m.loadSnapshot()
		m.clampSelection()
		if msg.err != nil {
			m.notice = msg.err.Error()
		} else {
			m.notice = "Quota data updated"
		}
		m.noticeUntil = m.now.Add(noticeDuration)
		m.clampScroll()
		return m, nil
	case tea.KeyMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

func (m *appModel) updateKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Type == tea.KeyCtrlC || key.String() == "q" {
		return m, tea.Quit
	}
	if key.String() == "?" && m.page != helpScreen {
		m.page = helpScreen
		m.scroll = 0
		return m, nil
	}
	if m.page == helpScreen {
		if key.Type == tea.KeyEsc || key.String() == "?" {
			m.page = overviewScreen
			m.scroll = 0
			return m, nil
		}
		m.scrollByKey(key)
		return m, nil
	}
	if key.String() == "R" {
		return m, m.startRefresh("")
	}
	if key.String() == "r" {
		if provider, ok := m.selectedProvider(); ok {
			return m, m.startRefresh(provider.ID)
		}
		return m, nil
	}
	if m.page == overviewScreen {
		switch key.Type {
		case tea.KeyUp:
			m.moveSelection(-1)
		case tea.KeyDown:
			m.moveSelection(1)
		case tea.KeyEnter:
			if len(m.providers) > 0 {
				m.page = detailScreen
				m.scroll = 0
			}
		case tea.KeyTab:
			m.moveSelection(1)
		case tea.KeyShiftTab:
			m.moveSelection(-1)
		case tea.KeyPgUp, tea.KeyPgDown:
			m.scrollByKey(key)
		default:
			switch key.String() {
			case "j":
				m.moveSelection(1)
			case "k":
				m.moveSelection(-1)
			}
		}
		return m, nil
	}
	switch key.Type {
	case tea.KeyEsc, tea.KeyLeft:
		m.page = overviewScreen
		m.scroll = 0
	case tea.KeyTab:
		m.moveSelection(1)
		m.scroll = 0
	case tea.KeyShiftTab:
		m.moveSelection(-1)
		m.scroll = 0
	case tea.KeyUp, tea.KeyDown, tea.KeyPgUp, tea.KeyPgDown:
		m.scrollByKey(key)
	default:
		switch key.String() {
		case "j":
			m.scroll++
		case "k":
			m.scroll--
		}
	}
	m.clampScroll()
	return m, nil
}

func (m *appModel) startRefresh(target string) tea.Cmd {
	if m.refreshing {
		m.notice = "A refresh is already running"
		m.noticeUntil = m.now.Add(noticeDuration)
		return nil
	}
	m.refreshing = true
	return func() tea.Msg {
		return refreshDoneMsg{err: m.refresh(m.ctx, target)}
	}
}

func (m *appModel) loadSnapshot() {
	providers, err := m.load()
	if err != nil {
		m.notice = "Cache read failed: " + err.Error()
		m.noticeUntil = m.now.Add(noticeDuration)
		return
	}
	m.providers = ordered(providers)
}

func (m *appModel) moveSelection(direction int) {
	if len(m.providers) == 0 {
		return
	}
	m.selected = (m.selected + direction + len(m.providers)) % len(m.providers)
	m.scroll = 0
}

func (m *appModel) clampSelection() {
	if len(m.providers) == 0 {
		m.selected = 0
		return
	}
	if m.selected >= len(m.providers) {
		m.selected = len(m.providers) - 1
	}
}

func (m *appModel) selectedProvider() (model.Provider, bool) {
	if m.selected < 0 || m.selected >= len(m.providers) {
		return model.Provider{}, false
	}
	return m.providers[m.selected], true
}

func (m *appModel) scrollByKey(key tea.KeyMsg) {
	pageSize := max(1, m.bodyHeight()-2)
	switch key.Type {
	case tea.KeyUp:
		m.scroll--
	case tea.KeyDown:
		m.scroll++
	case tea.KeyPgUp:
		m.scroll -= pageSize
	case tea.KeyPgDown:
		m.scroll += pageSize
	}
	m.clampScroll()
}

func (m *appModel) clampScroll() {
	lines := m.contentLines()
	maxScroll := max(0, len(lines)-m.bodyHeight())
	m.scroll = min(max(m.scroll, 0), maxScroll)
}

func (m *appModel) bodyHeight() int {
	return max(1, m.height-3)
}

func updateText(provider model.Provider, full bool, now time.Time) string {
	observed := provider.ObservedAt
	if observed == nil {
		observed = provider.LastSuccess
	}
	if observed == nil {
		return statusLabel(provider.Status)
	}
	if full {
		return "Last update " + observed.Local().Format("Mon 02 Jan 2006 15:04 MST")
	}
	updated := "Updated " + observed.Local().Format("15:04")
	switch provider.Status {
	case "healthy":
		return updated
	case "stale":
		return "Stale · " + age(*observed, now)
	case "failed":
		return "Failed · " + updated
	default:
		return statusLabel(provider.Status)
	}
}

func statusLabel(status string) string {
	switch status {
	case "healthy":
		return "Healthy"
	case "stale":
		return "Stale"
	case "failed":
		return "Update failed"
	case "unavailable":
		return "Unavailable"
	case "unauthenticated":
		return "Sign in required"
	case "unsupported":
		return "Unsupported"
	case "refreshing":
		return "Updating"
	case "degraded":
		return "Degraded"
	default:
		if status == "" {
			return "Unknown"
		}
		return strings.ToUpper(status)
	}
}

func age(observed, now time.Time) string {
	elapsed := now.Sub(observed)
	if elapsed < 0 {
		return "clock ahead"
	}
	if elapsed < time.Hour {
		return fmt.Sprintf("%dm ago", int(elapsed.Minutes()))
	}
	if elapsed < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(elapsed.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(elapsed.Hours()/24))
}

type windowGroup struct {
	name    string
	windows []model.Window
}

func groupWindows(windows []model.Window) []windowGroup {
	bucketIDs := make(map[string]struct{})
	for _, window := range windows {
		if window.Model == "" && window.BucketID != "" {
			bucketIDs[window.BucketID] = struct{}{}
		}
	}
	groups := make([]windowGroup, 0, len(windows))
	positions := make(map[string]int)
	for _, window := range windows {
		name := window.Model
		if name == "" && len(bucketIDs) > 1 {
			name = window.BucketID
		}
		index, ok := positions[name]
		if !ok {
			index = len(groups)
			positions[name] = index
			groups = append(groups, windowGroup{name: name})
		}
		groups[index].windows = append(groups[index].windows, window)
	}
	for index := range groups {
		sort.SliceStable(groups[index].windows, func(i, j int) bool {
			left, right := groups[index].windows[i].DurationMinutes, groups[index].windows[j].DurationMinutes
			if left == nil {
				return false
			}
			if right == nil {
				return true
			}
			return *left < *right
		})
	}
	return groups
}

func windowLabel(window model.Window) string {
	if window.DurationMinutes != nil {
		minutes := *window.DurationMinutes
		if minutes%1440 == 0 {
			return fmt.Sprintf("%dD", minutes/1440)
		}
		if minutes%60 == 0 {
			return fmt.Sprintf("%dH", minutes/60)
		}
		return fmt.Sprintf("%dM", minutes)
	}
	if window.Label != "" {
		return strings.ToUpper(window.Label)
	}
	if window.ID != "" {
		return strings.ToUpper(window.ID)
	}
	return "QUOTA"
}

func timeUntil(reset, now time.Time) string {
	if !reset.After(now) {
		return "reset passed"
	}
	return "in " + Countdown(reset.Sub(now))
}

func Countdown(remaining time.Duration) string {
	if remaining <= 0 {
		return "0m"
	}
	minutes := int(math.Ceil(remaining.Minutes()))
	if minutes < 60 {
		return fmt.Sprintf("%dm", minutes)
	}
	hours := minutes / 60
	minutes %= 60
	if hours < 24 {
		if minutes == 0 {
			return fmt.Sprintf("%dh", hours)
		}
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	days := hours / 24
	hours %= 24
	if hours == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd %dh", days, hours)
}

func resetText(reset, now time.Time) string {
	value := "Reset " + reset.Local().Format("Mon 02 Jan 2006 15:04 MST")
	if !reset.After(now) {
		return value + " · passed; waiting for provider data"
	}
	return value + " · " + timeUntil(reset, now)
}

func providerMetadata(provider model.Provider) string {
	parts := make([]string, 0, 2)
	if provider.Plan != "" {
		parts = append(parts, strings.ToUpper(provider.Plan))
	}
	if provider.Credits != nil {
		switch {
		case provider.Credits.Unlimited != nil && *provider.Credits.Unlimited:
			parts = append(parts, "UNLIMITED CREDITS")
		case provider.Credits.Balance != nil:
			parts = append(parts, "CREDITS "+*provider.Credits.Balance)
		case provider.Credits.HasCredits != nil && *provider.Credits.HasCredits:
			parts = append(parts, "CREDITS AVAILABLE")
		case provider.Credits.HasCredits != nil:
			parts = append(parts, "NO CREDITS")
		}
	}
	return strings.Join(parts, " · ")
}

func amountText(window model.Window) string {
	parts := make([]string, 0, 3)
	if window.UsedAmount != nil {
		parts = append(parts, fmt.Sprintf("%g used", *window.UsedAmount))
	}
	if window.RemainingAmount != nil {
		parts = append(parts, fmt.Sprintf("%g remaining", *window.RemainingAmount))
	}
	if window.AllocationLimit != nil {
		parts = append(parts, fmt.Sprintf("%g limit", *window.AllocationLimit))
	}
	value := strings.Join(parts, " · ")
	if window.Unit != "" {
		value += " " + window.Unit
	}
	return value
}

func truncate(value string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(value, width, "...")
}
