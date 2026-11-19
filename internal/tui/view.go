package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"aiquota/internal/model"
)

const wideLayoutWidth = 108
const sidebarWidth = 25

func (m *appModel) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	if m.width < 34 || m.height < 8 {
		return truncate("Resize terminal to 34×8 · q quits", m.width)
	}

	header := m.header()
	bodyHeight := max(1, m.height-lipgloss.Height(header)-1)
	var body string
	if m.width >= wideLayoutWidth && m.page != helpScreen {
		body = m.wideBody(bodyHeight)
	} else {
		body = m.narrowBody(bodyHeight)
	}
	footer := m.footer()
	return strings.Join([]string{header, body, footer}, "\n")
}

func (m *appModel) header() string {
	leftText := "AIQUOTA  SUBSCRIPTION USAGE"
	right := "LOCAL CACHE"
	if m.refreshing {
		right = "REFRESHING"
	} else if observed := latestObservation(m.providers); observed != nil {
		right = "UPDATED " + observed.Local().Format("15:04")
	}
	if lipgloss.Width(leftText)+lipgloss.Width(right)+1 > m.width {
		leftText = truncate(leftText, max(1, m.width-lipgloss.Width(right)-1))
	}
	left := m.styles.title.Render("AIQUOTA") + m.styles.muted.Render(strings.TrimPrefix(leftText, "AIQUOTA"))
	gap := max(1, m.width-lipgloss.Width(leftText)-lipgloss.Width(right))
	row := left + strings.Repeat(" ", gap) + m.styles.muted.Render(right)
	rule := m.styles.quiet.Render(strings.Repeat("─", m.width))
	return row + "\n" + rule
}

func (m *appModel) footer() string {
	text := "↑/↓ select  enter inspect  r refresh  R refresh all  ? help  q quit"
	if m.page == detailScreen {
		text = "esc overview  tab switch provider  ↑/↓ scroll  r refresh  ? help  q quit"
	} else if m.page == helpScreen {
		text = "esc overview  q quit"
	}
	if m.notice != "" {
		style := m.styles.muted
		if strings.Contains(strings.ToLower(m.notice), "failed") || strings.Contains(strings.ToLower(m.notice), "error") {
			style = m.styles.error
		}
		text = m.notice
		return style.Render(truncate(text, m.width))
	}
	if m.scroll > 0 {
		text = fmt.Sprintf("%s  %d/%d", text, m.scroll+1, max(1, len(m.contentLines())-m.bodyHeight()+1))
	}
	return m.styles.quiet.Render(truncate(text, m.width))
}

func (m *appModel) contentLines() []string {
	switch m.page {
	case detailScreen:
		provider, ok := m.selectedProvider()
		if !ok {
			return []string{"No provider selected"}
		}
		return m.providerLines(provider, m.contentWidth(), true)
	case helpScreen:
		return m.helpLines()
	default:
		if m.width >= wideLayoutWidth {
			provider, ok := m.selectedProvider()
			if !ok {
				return []string{"No providers enabled"}
			}
			return m.providerLines(provider, m.contentWidth(), false)
		}
		return m.overviewLines(m.width)
	}
}

func (m *appModel) contentWidth() int {
	if m.width >= wideLayoutWidth {
		return max(1, m.width-sidebarWidth-1-4)
	}
	return max(1, m.width-4)
}

func (m *appModel) narrowBody(height int) string {
	lines := m.contentLines()
	if m.page == overviewScreen {
		return m.viewport(lines, m.width, height)
	}
	innerWidth := max(1, m.width-4)
	innerHeight := max(1, height-2)
	frame := m.panel(m.width, height, m.page == detailScreen)
	return frame.Render(m.viewport(lines, innerWidth, innerHeight))
}

func (m *appModel) wideBody(height int) string {
	mainWidth := m.width - sidebarWidth - 1
	left := m.sidebar(height)
	provider, ok := m.selectedProvider()
	lines := []string{"Select a provider to view quota windows."}
	if ok {
		lines = m.providerLines(provider, max(1, mainWidth-4), m.page == detailScreen)
	}
	visible := m.viewport(lines, mainWidth-4, height-2)
	frame := m.panel(mainWidth, height, m.page == detailScreen)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, " ", frame.Render(visible))
}

func (m *appModel) sidebar(height int) string {
	innerWidth := sidebarWidth - 4
	lines := []string{m.styles.quiet.Render("PROVIDERS"), ""}
	for index, provider := range m.providers {
		name := truncate(strings.ToUpper(provider.Name), innerWidth-2)
		marker := "  "
		style := m.styles.title
		if index == m.selected {
			marker = m.styles.selected.Render("› ")
			style = m.styles.selected
		}
		lines = append(lines, marker+style.Render(name))
		lines = append(lines, "  "+m.statusStyle(provider.Status).Render("● "+statusLabel(provider.Status)))
		if metadata := providerMetadata(provider); metadata != "" {
			lines = append(lines, m.styles.quiet.Render(truncate(metadata, innerWidth)))
		}
		lines = append(lines, "")
	}
	lines = append(lines, "", m.styles.quiet.Render("↑/↓ select"), m.styles.quiet.Render("enter inspect"))
	return m.panel(sidebarWidth, height, false).Render(m.viewport(lines, innerWidth, height-2))
}

func (m *appModel) overviewLines(width int) []string {
	if len(m.providers) == 0 {
		return []string{m.styles.muted.Render("No providers enabled")}
	}
	lines := make([]string, 0, len(m.providers)*7)
	for index, provider := range m.providers {
		if index > 0 {
			lines = append(lines, "")
		}
		cardLines := m.providerLines(provider, width-4, false)
		panel := m.panel(width, lipgloss.Height(strings.Join(cardLines, "\n"))+2, index == m.selected)
		lines = append(lines, strings.Split(panel.Render(strings.Join(cardLines, "\n")), "\n")...)
	}
	return lines
}

func (m *appModel) providerLines(provider model.Provider, width int, detail bool) []string {
	name := m.styles.title.Render(strings.ToUpper(provider.Name))
	status := m.statusStyle(provider.Status).Render("● " + statusLabel(provider.Status))
	lines := []string{truncate(name+"  "+status, width)}
	if metadata := providerMetadata(provider); metadata != "" {
		lines = append(lines, m.styles.muted.Render(truncate(metadata, width)))
	}
	updated := updateText(provider, detail, m.now)
	if updated != "" {
		lines = append(lines, m.styles.quiet.Render(truncate(updated, width)))
	}
	if provider.Status != "healthy" && provider.Message != "" {
		lines = append(lines, m.styles.muted.Render(truncate(provider.Message, width)))
	}
	if len(provider.Windows) == 0 {
		message := "No quota data reported"
		if provider.Message != "" {
			message = provider.Message
		}
		lines = append(lines, "", m.styles.quiet.Render(truncate(message, width)))
		return lines
	}
	lines = append(lines, "")
	lines = append(lines, m.quotaGroups(groupWindows(provider.Windows), provider.Status, width, detail)...)
	return lines
}

func (m *appModel) quotaGroups(groups []windowGroup, status string, width int, detail bool) []string {
	lines := make([]string, 0)
	for _, group := range groups {
		if group.name != "" {
			lines = append(lines, m.styles.quiet.Render(strings.ToUpper(group.name)))
		}
		columns := 1
		if width >= 70 && len(group.windows) > 1 && !detail {
			columns = 2
		}
		cardWidth := width
		if columns == 2 {
			cardWidth = (width - 1) / 2
		} else if len(group.windows) == 1 {
			cardWidth = min(width, 48)
		}
		for index := 0; index < len(group.windows); index += columns {
			first := m.windowCard(group.windows[index], status, cardWidth, detail)
			row := first
			if columns == 2 && index+1 < len(group.windows) {
				second := m.windowCard(group.windows[index+1], status, cardWidth, detail)
				row = lipgloss.JoinHorizontal(lipgloss.Top, first, " ", second)
			}
			lines = append(lines, strings.Split(row, "\n")...)
		}
	}
	return lines
}

func (m *appModel) windowCard(window model.Window, status string, width int, detail bool) string {
	label := strings.ToUpper(windowLabel(window)) + " WINDOW"
	remaining := "N/A"
	percentStyle := m.styles.muted
	var percent float64
	known := window.RemainingPercent != nil && !math.IsNaN(*window.RemainingPercent) && !math.IsInf(*window.RemainingPercent, 0) && *window.RemainingPercent >= 0 && *window.RemainingPercent <= 100
	if known {
		percent = *window.RemainingPercent
		remaining = fmt.Sprintf("%.0f%% LEFT", percent)
		percentStyle = m.quotaStyle(percent, status)
		if percent <= m.options.WarningThreshold {
			percentStyle = percentStyle.Bold(true)
		}
	}
	innerWidth := max(8, width-4)
	lines := []string{truncate(m.styles.quiet.Render(label)+"  "+percentStyle.Render(remaining), innerWidth)}
	if known {
		lines = append(lines, m.meter(percent, status, innerWidth))
	} else {
		lines = append(lines, m.styles.quiet.Render("Usage unavailable"))
	}
	reset := "Reset time unavailable"
	if window.ResetsAt != nil {
		reset = "Resets " + timeUntil(*window.ResetsAt, m.now)
	}
	lines = append(lines, m.styles.muted.Render(truncate(reset, innerWidth)))
	if detail && window.ResetsAt != nil {
		lines = append(lines, m.styles.quiet.Render(truncate(resetText(*window.ResetsAt, m.now), innerWidth)))
	}
	if detail {
		if amounts := amountText(window); amounts != "" {
			lines = append(lines, m.styles.quiet.Render(truncate(amounts, innerWidth)))
		}
		if window.BucketID != "" && window.Model == "" {
			lines = append(lines, m.styles.quiet.Render(truncate("Bucket "+window.BucketID, innerWidth)))
		}
	}
	return m.panel(width, lipgloss.Height(strings.Join(lines, "\n"))+2, false).Render(strings.Join(lines, "\n"))
}

func (m *appModel) meter(percent float64, status string, width int) string {
	segments := min(22, max(8, width-1))
	filled := min(max(int(math.Round(percent*float64(segments)/100)), 0), segments)
	color := m.quotaStyle(percent, status)
	return color.Render(strings.Repeat("▰", filled)) + m.styles.quiet.Render(strings.Repeat("▱", segments-filled))
}

func (m *appModel) quotaStyle(percent float64, status string) lipgloss.Style {
	if status != "healthy" {
		return m.styles.muted
	}
	if percent < 30 {
		return m.styles.error
	}
	if percent < 60 {
		return m.styles.tertiary
	}
	return m.styles.primary
}

func (m *appModel) statusStyle(status string) lipgloss.Style {
	switch status {
	case "healthy":
		return m.styles.healthy
	case "stale", "degraded":
		return m.styles.warning
	case "failed":
		return m.styles.error
	default:
		return m.styles.muted
	}
}

func (m *appModel) panel(width, height int, selected bool) lipgloss.Style {
	style := m.styles.panel
	if selected {
		style = m.styles.focus
	}
	return style.Width(max(1, width-4)).Height(max(1, height-2))
}

func (m *appModel) viewport(lines []string, width, height int) string {
	if height < 1 {
		return ""
	}
	start := min(m.scroll, max(0, len(lines)-1))
	end := min(len(lines), start+height)
	visible := make([]string, 0, height)
	for _, line := range lines[start:end] {
		visible = append(visible, truncate(line, width))
	}
	for len(visible) < height {
		visible = append(visible, "")
	}
	return strings.Join(visible, "\n")
}

func (m *appModel) helpLines() []string {
	return []string{
		m.styles.title.Render("KEYBOARD"),
		"",
		"↑ / ↓    Select providers or scroll details",
		"j / k    Select providers or scroll details",
		"enter    Inspect the selected provider",
		"tab      Select the next provider",
		"shift+tab Select the previous provider",
		"r        Refresh the selected provider",
		"R        Refresh all active providers",
		"pgup/dn  Scroll one page",
		"esc      Return to the overview",
		"q        Quit",
		"",
		"Quota rows update from the local cache every five seconds.",
	}
}

func latestObservation(providers []model.Provider) *time.Time {
	var latest *time.Time
	for _, provider := range providers {
		observed := provider.ObservedAt
		if observed == nil {
			observed = provider.LastSuccess
		}
		if observed != nil && (latest == nil || observed.After(*latest)) {
			latest = observed
		}
	}
	return latest
}
