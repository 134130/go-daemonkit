package main

import (
	"context"
	"errors"
	"fmt"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	daemon "github.com/134130/go-daemonkit"
)

type model struct {
	epoch        uint64
	client       *daemon.Client
	ctx          context.Context
	session      *session
	size         Size
	showOverview bool
	busy         bool
	status       string
	overview     *overviewPanel
	logs         *logPanel
	spinner      spinner.Model
}

func newModel(ctx context.Context, session *session, builds []build) *model {
	return &model{ctx: ctx, session: session, showOverview: true, busy: true, status: "Connecting…", overview: &overviewPanel{builds: builds}, logs: newLogPanel(), spinner: spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(accent))}
}
func (m *model) command(action action) tea.Cmd {
	candidate := ""
	if len(m.overview.builds) > 0 {
		candidate = m.overview.builds[m.overview.selected].path
	}
	m.epoch++
	epoch, ctx, session, expected := m.epoch, m.ctx, m.session, m.overview.info.UpdateToken
	return func() tea.Msg {
		result := session.execute(ctx, action, candidate, expected)
		result.epoch = epoch
		return result
	}
}
func (m *model) Init() tea.Cmd { return tea.Batch(m.command(connectAction), m.spinner.Tick) }
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var command tea.Cmd
	var entry string
	switch message := msg.(type) {
	case tea.WindowSizeMsg:
		m.size = Size{message.Width, message.Height}
	case tea.KeyPressMsg:
		key := message.String()
		if key == "q" || key == "ctrl+c" {
			return m, tea.Quit
		}
		if key == "tab" {
			m.showOverview = !m.showOverview
		}
		if !m.busy {
			if key == "up" {
				m.overview.selected = max(0, m.overview.selected-1)
			}
			if key == "down" {
				m.overview.selected = min(len(m.overview.builds)-1, m.overview.selected+1)
			}
			action, ok := map[string]action{"c": connectAction, "enter": updateAction, "1": nowAction, "2": greetAction, "3": runtimeAction, "r": rawAction, "w": watchAction, "s": stopAction}[key]
			if action == updateAction {
				ok = m.overview.connected && len(m.overview.builds) > 0
				if ok && m.overview.builds[m.overview.selected].version == m.overview.info.Version {
					ok = false
				}
			}
			if ok {
				m.busy = true
				m.status = string(action) + "…"
				if action == updateAction {
					m.status = "Switching → " + m.overview.builds[m.overview.selected].version
				}
				entry = m.status
				command = tea.Batch(m.command(action), m.spinner.Tick)
			}
		}
	case disconnectedMsg:
		if message.client != m.client {
			return m, nil
		}
		m.client = nil
		m.overview.connected = false
		m.overview.info.State = daemon.StatusUnknown
		m.status = "Disconnected; press c to reconnect"
		entry = fmt.Sprintf("Connection closed: %v", message.client.Err())
	case resultMsg:
		if message.epoch != m.epoch {
			return m, nil
		}
		if message.client != nil && message.client.Err() != nil {
			message.connected = false
			message.info.State = daemon.StatusUnknown
			message.err = errors.Join(message.err, message.client.Err())
		}
		if message.builds != nil {
			m.overview.builds = message.builds
			m.overview.selected = 0
		}
		if message.client != m.client {
			m.client = message.client
			if message.connected {
				command = watchConnection(m.ctx, message.client)
			}
		}
		msg = message
		m.busy = false
		if message.err != nil {
			m.status = "Failed: " + string(message.action)
			entry = fmt.Sprintf("%s failed: %v", message.action, message.err)
			if errors.Is(message.err, daemon.ErrUpdateConflict) {
				m.status = "Daemon changed; review status and select a build again"
			}
		} else {
			m.status = "Ready"
			if !message.connected {
				m.status = "Offline"
			}
			entry = string(message.action) + ": " + message.detail
		}
	case spinner.TickMsg:
		if m.busy {
			m.spinner, command = m.spinner.Update(message)
		}
	}
	// Compute once after state changes; View uses the same pure layout function.
	sizes := layout(m.size, m.showOverview)
	overviewCmd := m.overview.Update(msg, sizes.overview)
	logCmd := m.logs.Update(msg, sizes.log)
	if entry != "" {
		m.logs.Update(logEntryMsg(entry), sizes.log)
	}
	return m, tea.Batch(command, overviewCmd, logCmd)
}
func (m *model) View() tea.View {
	sizes := layout(m.size, m.showOverview)
	body := m.logs.View(sizes.log)
	if m.showOverview {
		if sizes.horizontal {
			body = lipgloss.JoinHorizontal(lipgloss.Top, m.overview.View(sizes.overview), body)
		} else {
			body = lipgloss.JoinVertical(lipgloss.Left, m.overview.View(sizes.overview), body)
		}
	}
	status := m.status
	if m.busy {
		status = m.spinner.View() + " " + status
	}
	daemonState := string(m.overview.info.State)
	if daemonState == "" {
		daemonState = "unknown"
	}
	if m.overview.info.State == daemon.StatusRunning {
		daemonState = fmt.Sprintf("Running · not ready · PID %d", m.overview.info.PID)
	}
	if m.overview.info.State == daemon.StatusReady {
		daemonState = fmt.Sprintf("● Ready   Version %s   PID %d", m.overview.info.Version, m.overview.info.PID)
	}
	if m.busy {
		daemonState = status
	}
	header := fit(accent.Render("Daemon Update Demo")+"    CLI "+m.session.options.Version+"\nRunning Daemon  "+daemonState+"\n", Size{m.size.Width, 3})
	footer := fit(status+"\n"+muted.Render("↑↓ Build · Enter Switch · 1–3 RPC · r Raw · w Tick · s Stop · c Start · q Quit"), Size{m.size.Width, 2})
	view := tea.NewView(fit(lipgloss.JoinVertical(lipgloss.Left, header, body, footer), m.size))
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	return view
}

type disconnectedMsg struct{ client *daemon.Client }

func watchConnection(ctx context.Context, client *daemon.Client) tea.Cmd {
	return func() tea.Msg {
		select {
		case <-client.Done():
			return disconnectedMsg{client: client}
		case <-ctx.Done():
			return nil
		}
	}
}
