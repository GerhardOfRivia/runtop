package main

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Messages used for Bubbletea updates.
type TickMsg time.Time
type TelemetryMsg TelemetryData
type TelemetryErrMsg error

// SubprocessMsg is a sum type interface for process events.
type SubprocessMsg interface{}

// StdoutLineMsg represents a single line of stdout/stderr from the running command.
type StdoutLineMsg string

// SubprocessExitMsg represents the exit status of the running command.
type SubprocessExitMsg struct {
	Err error
}

// Model represents the Bubbletea application state.
type Model struct {
	viewport       viewport.Model
	collector      TelemetryCollector
	logger         *MultiCSVLogger
	cmdWriter      io.Writer
	command        string
	cmd            *exec.Cmd
	stdoutChan     chan SubprocessMsg
	telemetryData  TelemetryData
	ready          bool
	terminalWidth  int
	terminalHeight int
	logLines       []string
	exitErr        error
	startTime      time.Time
	endTime        time.Time
}

// NewModel initializes the Bubbletea Model.
func NewModel(collector TelemetryCollector, logger *MultiCSVLogger, cmdWriter io.Writer, command string) *Model {
	initialCPUs := make([]float64, 4)
	var initialGPUs []float64

	// Sync collect once to initialize layout correctly from start
	if data, err := collector.Collect(); err == nil {
		initialCPUs = data.CPUs
		initialGPUs = data.GPUs
	}

	return &Model{
		collector:      collector,
		logger:         logger,
		cmdWriter:      cmdWriter,
		command:        command,
		cmd:            exec.Command("sh", "-c", command),
		stdoutChan:     make(chan SubprocessMsg, 100),
		logLines:       []string{},
		terminalWidth:  80,
		terminalHeight: 24,
		telemetryData: TelemetryData{
			CPUs: initialCPUs,
			GPUs: initialGPUs,
		},
	}
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
		if err != nil {
			return TelemetryErrMsg(err)
		}
		if logger != nil {
			_ = logger.Log(data)
		}
		return TelemetryMsg(data)
	}
}

// listenForSubprocess waits for process stdout/exit messages on the channel.
func listenForSubprocess(ch chan SubprocessMsg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

// startSubprocess executes the process and pipes stdout/stderr to stdoutChan.
func (m *Model) startSubprocess() {
	pipeReader, pipeWriter := io.Pipe()
	m.cmd.Stdout = pipeWriter
	m.cmd.Stderr = pipeWriter

	m.startTime = time.Now()
	if err := m.cmd.Start(); err != nil {
		m.endTime = time.Now()
		m.stdoutChan <- SubprocessExitMsg{Err: err}
		_ = pipeWriter.Close()
		_ = pipeReader.Close()
		return
	}

	// Reader goroutine to scan output line by line
	go func() {
		scanner := bufio.NewScanner(pipeReader)
		for scanner.Scan() {
			m.stdoutChan <- StdoutLineMsg(scanner.Text())
		}
		_ = pipeReader.Close()
	}()

	err := m.cmd.Wait()
	m.endTime = time.Now()
	_ = pipeWriter.Close()
	m.stdoutChan <- SubprocessExitMsg{Err: err}
}

// Init initializes the Bubbletea program by launching the command.
func (m *Model) Init() tea.Cmd {
	// Start the command in the background
	go m.startSubprocess()

	return tea.Batch(
		tick(),
		listenForSubprocess(m.stdoutChan),
		collectTelemetry(m.collector, m.logger),
	)
}

// Update handles message updates to transition the application state.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			if m.cmd != nil && m.cmd.Process != nil {
				// Gracefully terminate the running subprocess on exit
				_ = m.cmd.Process.Kill()
			}
			return m, tea.Quit
		}

	case tea.WindowSizeMsg:
		m.handleWindowSize(msg)

	case TickMsg:
		cmds = append(cmds, tick(), collectTelemetry(m.collector, m.logger))

	case TelemetryMsg:
		m.telemetryData = TelemetryData(msg)

	case TelemetryErrMsg:
		// Ignore telemetry errors to keep command logs clean

	case StdoutLineMsg:
		m.appendLog(string(msg))
		cmds = append(cmds, listenForSubprocess(m.stdoutChan))

	case SubprocessExitMsg:
		m.exitErr = msg.Err
		if msg.Err != nil {
			m.appendLog(fmt.Sprintf("[process exited with error] %v", msg.Err))
		} else {
			m.appendLog("[process exited successfully]")
		}
		return m, tea.Quit
	}

	// Update the viewport component to handle scrolling inputs
	if m.ready {
		var vpCmd tea.Cmd
		m.viewport, vpCmd = m.viewport.Update(msg)
		cmds = append(cmds, vpCmd)
	}

	return m, tea.Batch(cmds...)
}

// handleWindowSize resizes the viewport based on screen dimensions.
func (m *Model) handleWindowSize(msg tea.WindowSizeMsg) {
	m.terminalWidth = msg.Width
	m.terminalHeight = msg.Height

	if !m.ready {
		m.viewport = viewport.New(msg.Width-2, 10)
		m.viewport.SetContent(strings.Join(m.logLines, "\n"))
		m.ready = true
	}
}

// appendLog appends a new stdout line to the viewport scroll buffer.
func (m *Model) appendLog(line string) {
	m.logLines = append(m.logLines, line)
	const maxScrollback = 1000
	if len(m.logLines) > maxScrollback {
		m.logLines = m.logLines[len(m.logLines)-maxScrollback:]
	}

	if m.ready {
		m.viewport.SetContent(strings.Join(m.logLines, "\n"))
		m.viewport.GotoBottom()
	}

	if m.cmdWriter != nil {
		_, _ = fmt.Fprintln(m.cmdWriter, line)
	}
}

// View renders the TUI splits and layouts.
func (m *Model) View() string {
	if !m.ready {
		return "initializing runtop tui dashboard...\n"
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

	statusBarStyle := lipgloss.NewStyle().
		Background(borderColor).
		Foreground(textColor).
		Padding(0, 1)

	helpBarStyle := lipgloss.NewStyle().
		Foreground(subtleColor).
		Padding(0, 1)

	viewportBorderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(purpleBrand)

	telemetryBorderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor)

	// 1. Title Bar
	logStatus := "logging disabled"
	if m.logger != nil {
		logStatus = fmt.Sprintf("logging: %s", m.logger.logDir)
	}
	headerText := fmt.Sprintf("runtop: %s [%s]", m.command, logStatus)
	header := titleStyle.Width(m.terminalWidth).Render(headerText)

	// 2. Compute Viewport and Telemetry heights dynamically
	// We budget terminalHeight - 1 to leave a 1-line safety margin at the bottom of the screen.
	// This prevents the terminal from auto-scrolling and clipping the top-level Title Bar (header).
	middleHeight := m.terminalHeight - 4
	if middleHeight < 8 {
		middleHeight = 8 // minimal fallback
	}

	// Sizing based on layout structure
	numCPUs := len(m.telemetryData.CPUs)
	if numCPUs == 0 {
		numCPUs = 4
	}
	numGPUs := len(m.telemetryData.GPUs)

	cpuLines := (numCPUs + 1) / 2
	sysLines := 2 + numGPUs // RAM + Disk + GPUs

	telemetryContentHeight := cpuLines
	if sysLines > telemetryContentHeight {
		telemetryContentHeight = sysLines
	}

	// Telemetry box height is telemetryContentHeight + 3 (title + borders)
	// vpBox height is viewportHeight + 4 (title + newline + borders)
	viewportHeight := middleHeight - telemetryContentHeight - 7

	if viewportHeight < 3 {
		viewportHeight = 3
	}

	// Update viewport dimension
	m.viewport.Width = m.terminalWidth - 2
	m.viewport.Height = viewportHeight

	// Top Half Viewport Box
	viewportTitle := lipgloss.NewStyle().Bold(true).Foreground(purpleBrand).Render(" command output ")
	vpBox := viewportBorderStyle.Width(m.terminalWidth - 2).Render(
		fmt.Sprintf("%s\n%s", viewportTitle, m.viewport.View()),
	)

	// Bottom Half Telemetry Box
	telemetryTitle := lipgloss.NewStyle().Bold(true).Foreground(subtleColor).Render(" system telemetry ")
	
	var telemetryBody string
	if m.terminalWidth > 50 {
		colWidth := (m.terminalWidth - 6) / 2
		leftCol := m.renderCPUsPanel(colWidth)
		rightCol := m.renderSystemPanel(colWidth)
		telemetryBody = lipgloss.JoinHorizontal(lipgloss.Top, leftCol, rightCol)
	} else {
		colWidth := m.terminalWidth - 4
		leftCol := m.renderCPUsPanel(colWidth)
		rightCol := m.renderSystemPanel(colWidth)
		telemetryBody = lipgloss.JoinVertical(lipgloss.Left, leftCol, "\n", rightCol)
	}

	telemetryBox := telemetryBorderStyle.Width(m.terminalWidth - 2).Render(
		fmt.Sprintf("%s\n%s", telemetryTitle, telemetryBody),
	)

	middleContent := lipgloss.JoinVertical(lipgloss.Left, vpBox, telemetryBox)

	// 3. Status Bar
	var statusMsg string
	if m.exitErr != nil {
		statusMsg = fmt.Sprintf("exited with error: %v", m.exitErr)
	} else if m.cmd != nil && m.cmd.ProcessState != nil && m.cmd.ProcessState.Exited() {
		statusMsg = "finished successfully"
	} else {
		statusMsg = "running..."
	}
	statusBarText := fmt.Sprintf(" %-50s", statusMsg)
	statusBar := statusBarStyle.Width(m.terminalWidth).Render(statusBarText)

	// 4. Help Bar
	helpText := " [q/ctrl+c] quit • up/down or mouse to scroll output"
	helpBar := helpBarStyle.Width(m.terminalWidth).Render(helpText)

	return lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		middleContent,
		statusBar,
		helpBar,
	)
}

// renderCPUsPanel renders the list of logical CPU cores.
func (m *Model) renderCPUsPanel(width int) string {
	var lines []string
	numCPUs := len(m.telemetryData.CPUs)

	successColor := lipgloss.Color("#50FA7B")

	if width > 40 {
		// Render CPU cores in 2 columns
		half := (numCPUs + 1) / 2
		for i := 0; i < half; i++ {
			leftCPU := i
			rightCPU := i + half
			
			leftStr := m.renderCoreLine(fmt.Sprintf("cpu%d", leftCPU), m.telemetryData.CPUs[leftCPU], successColor, width/2 - 1)
			rightStr := ""
			if rightCPU < numCPUs {
				rightStr = m.renderCoreLine(fmt.Sprintf("cpu%d", rightCPU), m.telemetryData.CPUs[rightCPU], successColor, width/2 - 1)
			}
			
			lines = append(lines, lipgloss.JoinHorizontal(lipgloss.Top, leftStr, " ", rightStr))
		}
	} else {
		// Render CPU cores in a single column
		for i := 0; i < numCPUs; i++ {
			lines = append(lines, m.renderCoreLine(fmt.Sprintf("cpu%d", i), m.telemetryData.CPUs[i], successColor, width))
		}
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(successColor)
	body := titleStyle.Render("logical cores") + "\n" + strings.Join(lines, "\n")
	return lipgloss.NewStyle().Width(width).Render(body)
}

// renderSystemPanel renders system RAM, Disk, and GPUs.
func (m *Model) renderSystemPanel(width int) string {
	var lines []string

	warnColor := lipgloss.Color("#FFB86C")
	errorColor := lipgloss.Color("#FF5555")
	promptColor := lipgloss.Color("#8BE9FD")

	// RAM
	lines = append(lines, m.renderCoreLine("RAM", m.telemetryData.RAM, warnColor, width))
	// Disk
	lines = append(lines, m.renderCoreLine("DSK", m.telemetryData.Disk, errorColor, width))

	// GPUs
	for i, gpuVal := range m.telemetryData.GPUs {
		lines = append(lines, m.renderCoreLine(fmt.Sprintf("GPU%d", i), gpuVal, promptColor, width))
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(promptColor)
	body := titleStyle.Render("SYSTEM & GPUs") + "\n" + strings.Join(lines, "\n")
	return lipgloss.NewStyle().Width(width).Render(body)
}

// renderCoreLine builds a single formatted column block for a metric.
func (m *Model) renderCoreLine(name string, value float64, color lipgloss.TerminalColor, width int) string {
	nameStyle := lipgloss.NewStyle().Bold(true).Foreground(color)
	valStr := fmt.Sprintf(" %5.1f%%", value)

	// Available space inside block
	contentWidth := width - 1
	barWidth := contentWidth - len(name) - len(valStr) - 1
	if barWidth < 3 {
		barWidth = 3 // ensure bar is visible
	}

	bar := drawProgressBar(value, barWidth, color)
	return fmt.Sprintf("%s %s%s", nameStyle.Render(name), bar, lipgloss.NewStyle().Foreground(color).Render(valStr))
}

// drawProgressBar renders a horizontal progress bar of a given width and color.
func drawProgressBar(percent float64, width int, activeColor lipgloss.TerminalColor) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	filledCount := int((percent / 100.0) * float64(width))
	if filledCount < 0 {
		filledCount = 0
	}
	if filledCount > width {
		filledCount = width
	}
	emptyCount := width - filledCount

	filledStyle := lipgloss.NewStyle().Foreground(activeColor)
	emptyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#3C3C3C")) // Dark gray block spacer

	filledStr := strings.Repeat("#", filledCount)
	emptyStr := strings.Repeat("-", emptyCount)

	return filledStyle.Render(filledStr) + emptyStyle.Render(emptyStr)
}
