package main

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/134130/go-daemonkit/examples/update-demo/api"
	"github.com/charmbracelet/x/ansi"
)

// Constraints are passed by parents. Only the root stores terminal dimensions.
type Size struct{ Width, Height int }
type Component interface {
	Update(tea.Msg, Size) tea.Cmd
	View(Size) string
}

type panelLayout struct {
	overview, log Size
	horizontal    bool
}

func layout(size Size, showOverview bool) panelLayout {
	body := Size{max(0, size.Width), max(0, size.Height-5)}
	if !showOverview {
		return panelLayout{log: body}
	}
	if body.Width >= 72 {
		return panelLayout{overview: Size{32, body.Height}, log: Size{body.Width - 32, body.Height}, horizontal: true}
	}
	top := min(15, max(0, body.Height-5))
	return panelLayout{overview: Size{body.Width, top}, log: Size{body.Width, body.Height - top}}
}

var (
	accent = lipgloss.NewStyle().Foreground(lipgloss.Color("#72D5C5")).Bold(true)
	muted  = lipgloss.NewStyle().Foreground(lipgloss.Color("#8C97AD"))
)

func fit(text string, size Size) string {
	if size.Width <= 0 || size.Height <= 0 {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) > size.Height {
		lines = lines[:size.Height]
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], size.Width, "…")
	}
	return lipgloss.NewStyle().Width(size.Width).Height(size.Height).Render(strings.Join(lines, "\n"))
}
func panel(title, body string, size Size) string {
	if size.Width < 4 || size.Height < 4 {
		return fit(body, size)
	}
	inner := Size{size.Width - 2, size.Height - 2}
	content := accent.Render(title) + "\n" + body
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#40516B")).Render(fit(content, inner))
}

type overviewPanel struct {
	builds    []build
	selected  int
	info      api.InfoResponse
	connected bool
}

func (p *overviewPanel) Update(msg tea.Msg, _ Size) tea.Cmd {
	if result, ok := msg.(resultMsg); ok {
		p.info, p.connected = result.info, result.connected
	}
	return nil
}
func (p *overviewPanel) hint() string {
	if len(p.builds) == 0 {
		return "No builds"
	}
	selected := p.builds[p.selected].version
	if !p.connected {
		return "Enter: start " + selected
	}
	if selected == p.info.Version {
		return "Already running"
	}
	if selected < p.info.Version {
		return "Enter: downgrade to " + selected
	}
	return "Enter: upgrade to " + selected
}
func (p *overviewPanel) View(size Size) string {
	var rows []string
	for i, b := range p.builds {
		cursor := " "
		if i == p.selected {
			cursor = "▸"
		}
		running := ""
		if p.connected && b.version == p.info.Version {
			running = " ● running"
		}
		rows = append(rows, cursor+" "+b.version+running)
	}
	builds := panel("Available Builds", strings.Join(rows, "\n")+"\n\n"+p.hint(), Size{size.Width, min(9, size.Height)})
	rpc := "\nRPC\n[1] Current time · v1+\n[2] Greeting     · v2+\n[3] Runtime info · v3+"
	return fit(builds+rpc, size)
}

type logEntryMsg string
type logPanel struct {
	viewport viewport.Model
	entries  []string
}

func newLogPanel() *logPanel { return &logPanel{viewport: viewport.New()} }
func (p *logPanel) Update(msg tea.Msg, size Size) tea.Cmd {
	// Synchronize before every message, including layout changes caused by Tab.
	oldWidth := p.viewport.Width()
	p.viewport.SetWidth(max(0, size.Width-2))
	p.viewport.SetHeight(max(0, size.Height-3))
	if oldWidth != p.viewport.Width() {
		p.viewport.SetContent(ansi.Wrap(strings.Join(p.entries, "\n"), max(1, p.viewport.Width()), ""))
	}
	if entry, ok := msg.(logEntryMsg); ok {
		p.entries = append(p.entries, time.Now().Format("15:04:05")+"  "+string(entry))
		if len(p.entries) > 200 {
			p.entries = p.entries[len(p.entries)-200:]
		}
		p.viewport.SetContent(ansi.Wrap(strings.Join(p.entries, "\n"), max(1, p.viewport.Width()), ""))
		p.viewport.GotoBottom()
		return nil
	}
	if key, ok := msg.(tea.KeyPressMsg); ok && (key.String() == "up" || key.String() == "down") {
		return nil
	}
	var cmd tea.Cmd
	p.viewport, cmd = p.viewport.Update(msg)
	return cmd
}
func (p *logPanel) View(size Size) string {
	return panel("Activity · PgUp/PgDn scroll", p.viewport.View(), size)
}

var _ Component = (*overviewPanel)(nil)
var _ Component = (*logPanel)(nil)
