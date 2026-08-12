package main

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	ansi "github.com/charmbracelet/x/ansi"
)

// Messages used for Bubbletea updates.
type TickMsg time.Time
type TelemetryMsg TelemetryData
type StdoutLineMsg string
type TelemetryResultMsg struct {
	Data TelemetryData
	Err  error
}

// Model represents the Bubbletea application state.
type Model struct {
	viewport         viewport.Model
	treeViewport     viewport.Model
	activeWindow     int // 0 = telemetry graphs (top), 1 = command output / process tree (bottom)
	collector        TelemetryCollector
	logger           *MultiCSVLogger
	command          string
	supervisor       *ProcessSupervisor
	rootPID          int
	telemetryData    TelemetryData
	telemetryErr     error
	sampling         bool
	ready            bool
	terminalWidth    int
	terminalHeight   int
	logLines         []string
	exitErr          error
	startTime        time.Time
	endTime          time.Time
	showProcessTree  bool
	processTreeLines []string
	confirmQuit      bool
	quitting         bool
	processRunning   bool
	processComplete  bool
}

// NewModel initializes the Bubbletea Model.
func NewModel(collector TelemetryCollector, logger *MultiCSVLogger, cmdWriter io.Writer, command string) *Model {
	initialData := TelemetryData{
		CPUs: make([]float64, 4),
	}

	model := &Model{
		collector:        collector,
		logger:           logger,
		command:          command,
		logLines:         []string{},
		terminalWidth:    80,
		terminalHeight:   24,
		telemetryData:    initialData,
		showProcessTree:  false,
		processTreeLines: []string{},
		activeWindow:     1,
		confirmQuit:      false,
		sampling:         true,
	}
	model.supervisor = NewProcessSupervisor(command, cmdWriter)
	return model
}

// Tick sets up the 1-second telemetry collection command.
func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return TickMsg(t)
	})
}

// collectTelemetry collects system telemetry asynchronously and writes to CSV.
func collectTelemetry(collector TelemetryCollector, logger *MultiCSVLogger) tea.Cmd {
	return func() tea.Msg {
		data, err := collector.Collect()
		if logger != nil && len(data.CPUs) > 0 {
			err = errors.Join(err, logger.Log(data))
		}
		return TelemetryResultMsg{Data: data, Err: err}
	}
}

// Init initializes the Bubbletea program by launching the command.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(
		tick(),
		m.supervisor.StartCmd(),
		collectTelemetry(m.collector, m.logger),
	)
}

// Update handles message updates to transition the application state.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.confirmQuit {
			switch msg.String() {
			case "y", "Y":
				return m.requestQuit()
			case "ctrl+c":
				return m.requestQuit()
			default:
				m.confirmQuit = false
			}
			return m, nil
		}

		switch msg.String() {
		case "q":
			if m.processComplete {
				return m, tea.Quit
			}
			m.confirmQuit = true
			return m, nil
		case "ctrl+c":
			return m.requestQuit()
		case "m", "M":
			m.showProcessTree = !m.showProcessTree
			if m.showProcessTree && m.rootPID > 0 && m.processRunning {
				cmds = append(cmds, collectProcessTree(m.rootPID))
			}
		}

	case ShutdownRequestMsg:
		return m.requestQuit()

	case tea.WindowSizeMsg:
		m.handleWindowSize(msg)

	case TickMsg:
		cmds = append(cmds, tick())
		if !m.sampling {
			m.sampling = true
			cmds = append(cmds, collectTelemetry(m.collector, m.logger))
		}
		if m.showProcessTree && m.rootPID > 0 && m.processRunning {
			cmds = append(cmds, collectProcessTree(m.rootPID))
		}

	case TelemetryResultMsg:
		m.sampling = false
		m.telemetryErr = msg.Err
		if len(msg.Data.CPUs) > 0 {
			m.telemetryData = msg.Data
		}

	case TelemetryMsg:
		// Retained for tests and callers that inject telemetry directly.
		m.telemetryData = TelemetryData(msg)

	case ProcessTreeMsg:
		m.processTreeLines = []string(msg)

	case SubprocessStartedMsg:
		m.rootPID = msg.PID
		m.startTime = msg.StartTime
		m.processRunning = true
		if collector, ok := m.collector.(TargetPIDCollector); ok {
			collector.SetTargetPID(int32(msg.PID))
		}
		cmds = append(cmds, m.supervisor.ListenCmd())
		if m.quitting {
			cmds = append(cmds, m.supervisor.StopCmd())
		}

	case OutputBatchMsg:
		m.appendLogBatch(msg.Lines)
		cmds = append(cmds, m.supervisor.ListenCmd())

	case StdoutLineMsg:
		// Compatibility for callers that inject a single output line.
		m.appendLog(string(msg))

	case SubprocessExitMsg:
		m.exitErr = msg.Err
		m.startTime = msg.StartTime
		m.endTime = msg.EndTime
		m.processRunning = false
		m.processComplete = true
		if collector, ok := m.collector.(TargetPIDCollector); ok {
			collector.SetTargetPID(0)
		}
		if msg.Err != nil {
			m.appendLog(fmt.Sprintf("[process exited with error] %v", msg.Err))
		} else {
			m.appendLog("[process exited successfully]")
		}
		if msg.StreamErr != nil {
			m.appendLog(fmt.Sprintf("[output capture warning] %v", msg.StreamErr))
		}
		if m.quitting {
			return m, tea.Quit
		}
	}

	// Update the active viewport component to handle scrolling inputs
	if m.ready {
		var vpCmd tea.Cmd
		if m.activeWindow == 1 {
			if m.showProcessTree {
				m.treeViewport, vpCmd = m.treeViewport.Update(msg)
			} else {
				m.viewport, vpCmd = m.viewport.Update(msg)
			}
			cmds = append(cmds, vpCmd)
		}
	}

	return m, tea.Batch(cmds...)
}

func (m *Model) requestQuit() (tea.Model, tea.Cmd) {
	m.confirmQuit = false
	m.quitting = true
	if m.processComplete {
		return m, tea.Quit
	}
	return m, m.supervisor.StopCmd()
}

// handleWindowSize resizes the viewport based on screen dimensions.
func (m *Model) handleWindowSize(msg tea.WindowSizeMsg) {
	m.terminalWidth = max(1, msg.Width)
	m.terminalHeight = max(1, msg.Height)

	if !m.ready {
		m.viewport = viewport.New(max(1, msg.Width-2), 10)
		m.viewport.SetContent(strings.Join(m.logLines, "\n"))
		m.treeViewport = viewport.New(max(1, msg.Width-2), 10)
		m.treeViewport.SetContent("")
		m.ready = true
	}
}

// appendLog appends a new stdout line to the viewport scroll buffer.
func (m *Model) appendLog(line string) {
	m.appendLogBatch([]string{line})
}

func (m *Model) appendLogBatch(lines []string) {
	if len(lines) == 0 {
		return
	}
	m.logLines = append(m.logLines, lines...)
	const maxScrollback = 1000
	if len(m.logLines) > maxScrollback {
		m.logLines = m.logLines[len(m.logLines)-maxScrollback:]
	}

	if m.ready {
		m.viewport.SetContent(strings.Join(m.logLines, "\n"))
		m.viewport.GotoBottom()
	}
}

// View renders the TUI splits and layouts.
func (m *Model) View() string {
	if !m.ready {
		return "initializing runtop tui dashboard...\n"
	}
	if m.terminalWidth < 30 || m.terminalHeight < 10 {
		status := "running"
		if m.quitting && !m.processComplete {
			status = "stopping"
		} else if m.exitErr != nil {
			status = "failed: " + m.exitErr.Error()
		} else if m.processComplete {
			status = "finished"
		}
		lines := []string{
			ansi.Truncate("runtop: "+m.command, m.terminalWidth, ""),
			ansi.Truncate(status, m.terminalWidth, ""),
		}
		if m.terminalHeight > 2 {
			lines = append(lines, ansi.Truncate("[q] quit", m.terminalWidth, ""))
		}
		return strings.Join(lines[:min(len(lines), m.terminalHeight)], "\n")
	}

	// Brand Colors from runbook
	purpleBrand := lipgloss.Color("#7D56F4")
	borderColor := lipgloss.Color("#3C3C3C")
	subtleColor := lipgloss.Color("#6272A4")
	textColor := lipgloss.Color("#D9D9D9")

	// Styles
	titleStyle := lipgloss.NewStyle().
		Background(purpleBrand).
		Foreground(lipgloss.Color("#FFFFFF")).
		Bold(true).
		Padding(0, 1)

	var topBorderColor lipgloss.Color = borderColor
	var bottomBorderColor lipgloss.Color = purpleBrand

	topBorderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(topBorderColor)

	bottomBorderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(bottomBorderColor)

	// 1. Title Bar
	logStatus := "logging disabled"
	if m.logger != nil {
		logStatus = fmt.Sprintf("logging: %s", m.logger.logDir)
	}
	modeName := "command output"
	if m.showProcessTree {
		modeName = "process tree"
	}
	headerText := fmt.Sprintf("runtop: %s [%s] | mode: %s", m.command, logStatus, modeName)
	headerText = ansi.Truncate(headerText, max(1, m.terminalWidth-4), "...")
	header := titleStyle.Width(m.terminalWidth - 2).Render(headerText)

	// 2. Compute Viewport and Telemetry heights dynamically
	// We budget terminalHeight - 1 to leave a 1-line safety margin at the bottom of the screen.
	// This prevents the terminal from auto-scrolling and clipping the top-level Title Bar (header).
	middleHeight := m.terminalHeight - 3
	if middleHeight < 8 {
		middleHeight = 8 // minimal fallback
	}

	// Sizing based on layout structure
	numCPUs := len(m.telemetryData.CPUs)

	if numCPUs == 0 {
		return "Error: Not enough telemetry data to render graphs"
	}

	innerWidth := m.terminalWidth - 4
	if innerWidth < 8 {
		innerWidth = 8 // minimal fallback
	}

	successColor := lipgloss.Color("#50FA7B")
	errorColor := lipgloss.Color("#FF5555")
	warnColor := lipgloss.Color("#FFB86C")
	promptColor := lipgloss.Color("#8BE9FD")
	telemetryBody := m.renderTelemetryPanel(innerWidth, successColor, promptColor, warnColor, errorColor, subtleColor, textColor)
	telemetryContentHeight := lipgloss.Height(telemetryBody)

	// Graphs box height is telemetryContentHeight + 2 (borders, no title)
	// bottomBox height is viewportHeight + 4 (title/header + newline + borders)
	viewportHeight := middleHeight - telemetryContentHeight - 6

	if viewportHeight < 3 {
		viewportHeight = 3
	}

	// Update viewport dimension
	m.viewport.Width = m.terminalWidth - 2
	m.viewport.Height = viewportHeight

	// Top Half Graphs Box (always graphs on top)
	graphsBox := topBorderStyle.Width(m.terminalWidth - 2).Render(telemetryBody)

	var bottomBox string
	headerStyle := lipgloss.NewStyle().Bold(true).Background(promptColor).Foreground(lipgloss.Color("#FFFFFF"))
	if m.showProcessTree {
		headerText := fmt.Sprintf(" %5s %-8s %5s %5s %8s %s", "pid", "user", "cpu%", "mem%", "time", "command")
		headerLine := headerStyle.Width(m.terminalWidth - 4).Render(headerText)

		var body string
		if len(m.processTreeLines) == 0 {
			body = "collecting process tree data..."
			// Pad to viewportHeight
			for i := 1; i < viewportHeight; i++ {
				body += "\n"
			}
		} else {
			// Update the tree viewport dimensions
			m.treeViewport.Width = m.terminalWidth - 4
			m.treeViewport.Height = viewportHeight

			// Set content on the tree viewport
			m.treeViewport.SetContent(strings.Join(m.processTreeLines, "\n"))

			// Viewport render
			body = headerLine + "\n" + m.treeViewport.View()
		}

		bottomBox = bottomBorderStyle.Width(m.terminalWidth - 2).Render(body)
	} else {

		headerLine := headerStyle.Width(m.terminalWidth - 4).Render("command output")

		bottomBox = bottomBorderStyle.Width(m.terminalWidth - 2).Render(
			fmt.Sprintf("%s\n%s", headerLine, m.viewport.View()),
		)
	}

	// 3. Footer Bar (Combined Status & Help Bar)
	var footer string
	if m.confirmQuit {
		promptStyle := lipgloss.NewStyle().
			Background(lipgloss.Color("#FF5555")).
			Foreground(lipgloss.Color("#FFFFFF")).
			Bold(true).
			Padding(0, 1)
		promptText := "Are you sure you want to quit? (y/N)"
		helpText := "Press [y] to confirm quit, or [n] to cancel"

		promptRendered := promptStyle.Render(promptText)
		helpRendered := lipgloss.NewStyle().
			Background(borderColor).
			Foreground(textColor).
			Padding(0, 1).
			Render(helpText)

		combinedText := promptRendered + helpRendered
		combinedWidth := lipgloss.Width(combinedText)
		if combinedWidth < m.terminalWidth-2 {
			paddingWidth := m.terminalWidth - 2 - combinedWidth
			footer = combinedText + lipgloss.NewStyle().Background(borderColor).Render(strings.Repeat(" ", paddingWidth))
		} else {
			footer = combinedText
		}
	} else {
		var statusMsg string
		if m.quitting && !m.processComplete {
			statusMsg = "stopping process tree..."
		} else if m.exitErr != nil {
			statusMsg = fmt.Sprintf("exited with error: %v", m.exitErr)
		} else if m.processComplete {
			statusMsg = "finished successfully"
		} else if m.telemetryErr != nil {
			statusMsg = fmt.Sprintf("telemetry warning: %v", m.telemetryErr)
		} else {
			statusMsg = "running..."
		}

		statusText := fmt.Sprintf(" %s", statusMsg)

		var helpText string
		if m.terminalWidth-2 > len(statusText)+63 {
			helpText = "[q/ctrl+c] quit • [m] toggle view • up/down or mouse to scroll"
		} else if m.terminalWidth-2 > len(statusText)+39 {
			helpText = "[q] quit • [m] toggle • up/down scroll"
		} else if m.terminalWidth-2 > len(statusText)+22 {
			helpText = "[q] quit • [m] toggle"
		} else {
			helpText = "[q] quit"
		}

		statusStyle := lipgloss.NewStyle().Foreground(textColor).Bold(true)
		helpStyle := lipgloss.NewStyle().Foreground(subtleColor)

		statusRendered := statusStyle.Render(statusText)
		helpRendered := helpStyle.Render(helpText)

		statusWidth := lipgloss.Width(statusRendered)
		helpWidth := lipgloss.Width(helpRendered)

		combinedWidth := statusWidth + helpWidth + 2
		var combinedText string
		if combinedWidth < m.terminalWidth-2 {
			paddingWidth := m.terminalWidth - 2 - statusWidth - helpWidth - 1
			if paddingWidth < 0 {
				paddingWidth = 0
			}
			combinedText = statusRendered + strings.Repeat(" ", paddingWidth) + helpRendered + " "
		} else {
			combinedText = statusRendered + " " + helpRendered
		}

		footer = lipgloss.NewStyle().Background(borderColor).Width(m.terminalWidth - 2).Render(combinedText)
	}

	return fmt.Sprintf("%s\n%s\n%s\n%s", header, graphsBox, bottomBox, footer)
}

const (
	metricLabelWidth = 10
	metricColumnGap  = 2
	metricMaxColumns = 4
	metricMinBar     = 8
	metricMaxBar     = 30
)

type metricItem struct {
	name  string
	value float64
	label string
	color lipgloss.TerminalColor
}

type metricGridLayout struct {
	columns   int
	cellWidth int
	barWidth  int
}

func newMetricGridLayout(width, maxItems int) metricGridLayout {
	columns := min(metricMaxColumns, max(1, maxItems))
	for columns > 1 {
		cellWidth := (width - metricColumnGap*(columns-1)) / columns
		if cellWidth-metricLabelWidth-3 >= metricMinBar {
			break
		}
		columns--
	}
	cellWidth := max(metricLabelWidth+3+metricMinBar, (width-metricColumnGap*(columns-1))/columns)
	barWidth := min(metricMaxBar, max(metricMinBar, cellWidth-metricLabelWidth-3))
	return metricGridLayout{columns: columns, cellWidth: cellWidth, barWidth: barWidth}
}

func (m *Model) renderTelemetryPanel(width int, cpuColor, gpuColor, memoryColor, diskColor, keyColor, valueColor lipgloss.TerminalColor) string {
	cpuItems := make([]metricItem, 0, len(m.telemetryData.CPUs))
	for index, value := range m.telemetryData.CPUs {
		cpuItems = append(cpuItems, metricItem{
			name:  fmt.Sprintf("cpu%d", index),
			value: value,
			label: fmt.Sprintf("%.1f%%", value),
			color: cpuColor,
		})
	}

	gpuItems := make([]metricItem, 0, len(m.telemetryData.GPUs))
	for index, value := range m.telemetryData.GPUs {
		gpuItems = append(gpuItems, metricItem{
			name:  fmt.Sprintf("gpu%d", index),
			value: value,
			label: fmt.Sprintf("%.1f%%", value),
			color: gpuColor,
		})
	}

	memoryLabel := fmt.Sprintf("%.1f%%", m.telemetryData.RAM)
	if m.telemetryData.RAMTotal > 0 {
		memoryLabel = formatRAMUsage(m.telemetryData.RAMUsed, m.telemetryData.RAMTotal)
	}
	storageItems := []metricItem{
		{name: "memory", value: m.telemetryData.RAM, label: memoryLabel, color: memoryColor},
		{name: "swap", value: m.telemetryData.Swap, label: fmt.Sprintf("%.1f%%", m.telemetryData.Swap), color: memoryColor},
	}
	if len(m.telemetryData.Disks) == 0 {
		label := fmt.Sprintf("%.1f%%", m.telemetryData.Disk)
		if m.telemetryData.DiskTotal > 0 {
			label = formatRAMUsage(m.telemetryData.DiskUsed, m.telemetryData.DiskTotal)
		}
		storageItems = append(storageItems, metricItem{name: "/", value: m.telemetryData.Disk, label: label, color: diskColor})
	} else {
		for _, disk := range m.telemetryData.Disks {
			label := fmt.Sprintf("%.1f%%", disk.UsedPercent)
			if disk.Total > 0 {
				label = formatRAMUsage(disk.Used, disk.Total)
			}
			storageItems = append(storageItems, metricItem{name: disk.Mountpoint, value: disk.UsedPercent, label: label, color: diskColor})
		}
	}

	targetItems := make([]metricItem, 0, 2)
	if target := m.telemetryData.Target; target.PID > 0 {
		rssPercent := 0.0
		if m.telemetryData.RAMTotal > 0 {
			rssPercent = float64(target.RSSBytes) / float64(m.telemetryData.RAMTotal) * 100
		}
		targetItems = append(targetItems,
			metricItem{name: "proc cpu", value: target.CPUPercent, label: fmt.Sprintf("%.1f%%", target.CPUPercent), color: gpuColor},
			metricItem{name: "proc rss", value: rssPercent, label: formatBytesCompact(target.RSSBytes), color: gpuColor},
		)
	}

	maxItems := max(len(cpuItems), len(gpuItems), len(storageItems), len(targetItems))
	layout := newMetricGridLayout(width, maxItems)
	groups := [][]metricItem{cpuItems, gpuItems, storageItems, targetItems}
	lines := make([]string, 0)
	for _, group := range groups {
		lines = append(lines, renderMetricGrid(group, layout)...)
	}

	keyStyle := lipgloss.NewStyle().Bold(true).Foreground(keyColor)
	valueStyle := lipgloss.NewStyle().Foreground(valueColor)
	summary := keyStyle.Render("uptime:") + " " + valueStyle.Render(formatUptime(m.telemetryData.Uptime)) +
		"  " + keyStyle.Render("load:") + " " + valueStyle.Render(fmt.Sprintf("%.2f %.2f %.2f", m.telemetryData.Load1, m.telemetryData.Load5, m.telemetryData.Load15))
	lines = append(lines, ansi.Truncate(summary, width, "..."))
	if target := m.telemetryData.Target; target.PID > 0 {
		targetSummary := keyStyle.Render("target") + " " + valueStyle.Render(fmt.Sprintf("pid %d  %d procs  read %s  write %s", target.PID, target.Processes, formatBytesCompact(target.ReadBytes), formatBytesCompact(target.WriteBytes)))
		lines = append(lines, ansi.Truncate(targetSummary, width, "..."))
	}
	return strings.Join(lines, "\n")
}

func renderMetricGrid(items []metricItem, layout metricGridLayout) []string {
	if len(items) == 0 {
		return nil
	}
	rows := (len(items) + layout.columns - 1) / layout.columns
	lines := make([]string, 0, rows)
	for row := 0; row < rows; row++ {
		cells := make([]string, 0, layout.columns)
		for column := 0; column < layout.columns; column++ {
			index := row*layout.columns + column
			if index >= len(items) {
				break
			}
			cell := renderMetricCell(items[index], layout)
			cell += strings.Repeat(" ", max(0, layout.cellWidth-lipgloss.Width(cell)))
			cells = append(cells, cell)
		}
		lines = append(lines, strings.TrimRight(strings.Join(cells, strings.Repeat(" ", metricColumnGap)), " "))
	}
	return lines
}

func renderMetricCell(item metricItem, layout metricGridLayout) string {
	name := ansi.Truncate(item.name, metricLabelWidth, "...")
	name += strings.Repeat(" ", max(0, metricLabelWidth-lipgloss.Width(name)))
	name = lipgloss.NewStyle().Bold(true).Foreground(item.color).Render(name)
	return name + " " + renderProgressBarWithOverlay(item.value, item.label, item.color, layout.barWidth)
}

// renderProgressBarWithOverlay builds a progress bar enclosed in [ and ] with active segments filled with '|',
// empty segments filled with ' ', and an optional label overlaid on the right side.
func renderProgressBarWithOverlay(value float64, label string, color lipgloss.TerminalColor, barWidth int) string {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		value = 0
	}
	if value < 0 {
		value = 0
	}
	if value > 100 {
		value = 100
	}

	filledCount := int((value / 100.0) * float64(barWidth))
	if filledCount < 0 {
		filledCount = 0
	}
	if filledCount > barWidth {
		filledCount = barWidth
	}

	filledStyle := lipgloss.NewStyle().Foreground(color)
	emptyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#3C3C3C"))
	bracketStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6272A4"))
	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#D9D9D9"))

	label = ansi.Truncate(label, barWidth, "")
	labelWidth := lipgloss.Width(label)
	var coloredBar string
	if labelWidth <= barWidth {
		// Overlay the label at the right end while retaining fill information beneath it.
		startIdx := barWidth - labelWidth

		// Part of the bar before the label
		leftFilled := filledCount
		if leftFilled > startIdx {
			leftFilled = startIdx
		}
		leftEmpty := startIdx - leftFilled

		filledStr := strings.Repeat("|", leftFilled)
		emptyStr := strings.Repeat(" ", leftEmpty)

		filledLabelWidth := min(labelWidth, max(0, filledCount-startIdx))
		labelRunes := []rune(label)
		filledLabel := string(labelRunes[:min(filledLabelWidth, len(labelRunes))])
		emptyLabel := string(labelRunes[min(filledLabelWidth, len(labelRunes)):])
		coloredBar = filledStyle.Render(filledStr) + emptyStyle.Render(emptyStr) + filledStyle.Render(filledLabel) + textStyle.Render(emptyLabel)
	} else {
		// Fallback if label is too long
		filledStr := strings.Repeat("|", filledCount)
		emptyStr := strings.Repeat(" ", barWidth-filledCount)
		coloredBar = filledStyle.Render(filledStr) + emptyStyle.Render(emptyStr)
	}

	return bracketStyle.Render("[") + coloredBar + bracketStyle.Render("]")
}

func formatUptime(seconds uint64) string {
	days := seconds / 86400
	hours := (seconds % 86400) / 3600
	minutes := (seconds % 3600) / 60
	sec := seconds % 60
	if days > 0 {
		return fmt.Sprintf("up %dd %dh %dm %ds", days, hours, minutes, sec)
	}
	if hours > 0 {
		return fmt.Sprintf("up %dh %dm %ds", hours, minutes, sec)
	}
	return fmt.Sprintf("up %dm %ds", minutes, sec)
}

func formatBytesCompact(bytes uint64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)
	if bytes >= GB {
		val := float64(bytes) / float64(GB)
		if val == float64(int(val)) {
			return fmt.Sprintf("%.0fG", val)
		}
		return fmt.Sprintf("%.1fG", val)
	}
	if bytes >= MB {
		val := float64(bytes) / float64(MB)
		if val == float64(int(val)) {
			return fmt.Sprintf("%.0fM", val)
		}
		return fmt.Sprintf("%.1fM", val)
	}
	if bytes >= KB {
		return fmt.Sprintf("%dK", bytes/KB)
	}
	return fmt.Sprintf("%dB", bytes)
}

func formatRAMUsage(used, total uint64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)

	// If both are GB
	if used >= GB && total >= GB {
		usedVal := float64(used) / float64(GB)
		totalVal := float64(total) / float64(GB)
		var usedStr, totalStr string
		if usedVal == float64(int(usedVal)) {
			usedStr = fmt.Sprintf("%.0f", usedVal)
		} else {
			usedStr = fmt.Sprintf("%.1f", usedVal)
		}
		if totalVal == float64(int(totalVal)) {
			totalStr = fmt.Sprintf("%.0fG", totalVal)
		} else {
			totalStr = fmt.Sprintf("%.1fG", totalVal)
		}
		return fmt.Sprintf("%s/%s", usedStr, totalStr)
	}

	// If both are MB
	if used >= MB && total >= MB {
		usedVal := float64(used) / float64(MB)
		totalVal := float64(total) / float64(MB)
		var usedStr, totalStr string
		if usedVal == float64(int(usedVal)) {
			usedStr = fmt.Sprintf("%.0f", usedVal)
		} else {
			usedStr = fmt.Sprintf("%.1f", usedVal)
		}
		if totalVal == float64(int(totalVal)) {
			totalStr = fmt.Sprintf("%.0fM", totalVal)
		} else {
			totalStr = fmt.Sprintf("%.1fM", totalVal)
		}
		return fmt.Sprintf("%s/%s", usedStr, totalStr)
	}

	// Fallback to independent formatting
	return fmt.Sprintf("%s/%s", formatBytesCompact(used), formatBytesCompact(total))
}

// ProcessInfo holds information about a single process in the tree.
type ProcessInfo struct {
	Pid     int
	Ppid    int
	User    string
	CPU     float64
	Mem     float64
	Time    string
	Command string
}

// ProcessTreeMsg carries the process tree output lines.
type ProcessTreeMsg []string

// collectProcessTree initiates the process tree collection command.
func collectProcessTree(pid int) tea.Cmd {
	return func() tea.Msg {
		lines, err := getProcessTree(pid)
		if err != nil {
			return ProcessTreeMsg{}
		}
		return ProcessTreeMsg(lines)
	}
}

// getProcessTree runs `ps` and returns the formatted process tree lines starting from targetPid.
func getProcessTree(targetPid int) ([]string, error) {
	cmd := exec.Command("ps", "-axw", "-o", "pid,ppid,user,%cpu,%mem,time,args")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	procMap := make(map[int]ProcessInfo)
	children := make(map[int][]int)

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "PID") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}

		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		user := fields[2]
		cpu, err := strconv.ParseFloat(fields[3], 64)
		if err != nil {
			continue
		}
		mem, err := strconv.ParseFloat(fields[4], 64)
		if err != nil {
			continue
		}
		timeStr := fields[5]
		command := strings.Join(fields[6:], " ")

		proc := ProcessInfo{
			Pid:     pid,
			Ppid:    ppid,
			User:    user,
			CPU:     cpu,
			Mem:     mem,
			Time:    timeStr,
			Command: command,
		}

		procMap[pid] = proc
		children[ppid] = append(children[ppid], pid)
	}

	var result []string
	if _, exists := procMap[targetPid]; exists {
		buildTree(targetPid, 0, []bool{true}, children, procMap, &result)
	}

	return result, nil
}

// buildTree recursively constructs the tree layout lines.
func buildTree(pid int, level int, isLast []bool, children map[int][]int, procMap map[int]ProcessInfo, result *[]string) {
	proc, exists := procMap[pid]
	if !exists {
		return
	}

	var prefix strings.Builder
	// Build prefix for ancestors
	for p := 1; p < level; p++ {
		if isLast[p] {
			prefix.WriteString("   ")
		} else {
			prefix.WriteString("│  ")
		}
	}
	// Add branch symbol for the current level (if level > 0)
	if level > 0 {
		if isLast[level] {
			prefix.WriteString("└─ ")
		} else {
			prefix.WriteString("├─ ")
		}
	}

	// Format process tree line: PID USER CPU% MEM% TIME COMMAND
	line := fmt.Sprintf("%6d %-8s %5.1f %5.1f %8s %s%s", proc.Pid, proc.User, proc.CPU, proc.Mem, proc.Time, prefix.String(), proc.Command)
	*result = append(*result, line)

	cList := children[pid]
	sort.Ints(cList)
	for idx, childPid := range cList {
		isLastChild := idx == len(cList)-1
		buildTree(childPid, level+1, append(isLast, isLastChild), children, procMap, result)
	}
}
