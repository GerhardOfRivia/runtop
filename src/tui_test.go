package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// DummyCollector implements TelemetryCollector with static values.
type DummyCollector struct{}

func (d *DummyCollector) Collect() (TelemetryData, error) {
	return TelemetryData{
		CPUs: []float64{10.0, 15.0},
		RAM:  20.0,
		GPUs: []float64{30.0, 35.0},
		Disk: 40.0,
	}, nil
}

func TestNewModel(t *testing.T) {
	collector := &DummyCollector{}
	model := NewModel(collector, nil, nil, "echo test")

	if model.collector != collector {
		t.Error("Expected collector to be set")
	}

	if len(model.logLines) != 0 {
		t.Errorf("Expected 0 initial log lines, got %d", len(model.logLines))
	}
}

func TestDrawProgressBar(t *testing.T) {
	tests := []struct {
		percent  float64
		width    int
		expected string
	}{
		{percent: 0, width: 10, expected: "[          ]"},
		{percent: 100, width: 10, expected: "[||||||||||]"},
		{percent: 50, width: 10, expected: "[|||||     ]"},
		{percent: -10, width: 5, expected: "[     ]"},
		{percent: 120, width: 5, expected: "[|||||]"},
	}

	for _, tt := range tests {
		rawOutput := renderProgressBarWithOverlay(tt.percent, "", nil, tt.width)
		cleanOutput := stripANSI(rawOutput)
		if cleanOutput != tt.expected {
			t.Errorf("For %f%% at width %d, expected %q, got %q", tt.percent, tt.width, tt.expected, cleanOutput)
		}
	}
}

// Helper to strip ANSI codes for testing
func stripANSI(str string) string {
	var sb strings.Builder
	inEscape := false
	for i := 0; i < len(str); i++ {
		if str[i] == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if str[i] == 'm' {
				inEscape = false
			}
			continue
		}
		sb.WriteByte(str[i])
	}
	return sb.String()
}

func TestModelUpdate(t *testing.T) {
	collector := &DummyCollector{}
	model := NewModel(collector, nil, nil, "echo test")

	// Trigger WindowSizeMsg
	var cmd tea.Cmd
	var rawModel tea.Model
	rawModel, cmd = model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := rawModel.(*Model)

	if !m.ready {
		t.Error("Model should be ready after WindowSizeMsg")
	}
	_ = cmd // cmd is viewport setup cmd

	// Update telemetry
	telemetryUpdate := TelemetryMsg{
		CPUs: []float64{55.5, 60.0},
		RAM:  66.6,
		GPUs: []float64{77.7, 80.0},
		Disk: 88.8,
	}
	rawModel, _ = m.Update(telemetryUpdate)
	m = rawModel.(*Model)

	if m.telemetryData.CPUs[0] != 55.5 {
		t.Errorf("Expected CPU to update to 55.5, got %v", m.telemetryData.CPUs[0])
	}

	// Update stdout log line
	logCountBefore := len(m.logLines)
	rawModel, _ = m.Update(StdoutLineMsg("mock log line"))
	m = rawModel.(*Model)

	if len(m.logLines) != logCountBefore+1 {
		t.Errorf("Expected log line count to increase to %d, got %d", logCountBefore+1, len(m.logLines))
	}
}

func TestCommandLogging(t *testing.T) {
	var buf strings.Builder
	supervisor := NewProcessSupervisor("echo test", &buf)
	if err := supervisor.scanOutput(strings.NewReader("test line 1\ntest line 2\n")); err != nil {
		t.Fatalf("scanOutput failed: %v", err)
	}

	expected := "test line 1\ntest line 2\n"
	if buf.String() != expected {
		t.Errorf("Expected logs to be written to command log file:\n%q\nGot:\n%q", expected, buf.String())
	}
}

func TestWriteMetaLog(t *testing.T) {
	tmpFile := "test_runtop_meta.txt"
	defer os.Remove(tmpFile)

	collector := &DummyCollector{}
	model := NewModel(collector, nil, nil, "echo test")

	model.startTime = time.Unix(1782931708, 230662360)
	model.endTime = time.Unix(1782932591, 599932861)
	model.exitErr = nil

	err := writeMetaLog(tmpFile, model)
	if err != nil {
		t.Fatalf("Failed to write meta log: %v", err)
	}

	contentBytes, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("Failed to read back meta log: %v", err)
	}

	content := string(contentBytes)
	expectedLines := []string{
		"start(s.n) 1782931708.230662360",
		"return(code) 0",
		"end(s.n) 1782932591.599932861",
		"runtime(sec) 883.369270501",
	}

	for _, expected := range expectedLines {
		if !strings.Contains(content, expected) {
			t.Errorf("Expected metadata file to contain %q, but got:\n%s", expected, content)
		}
	}
}

func TestViewHeader(t *testing.T) {
	collector := &DummyCollector{}
	model := NewModel(collector, nil, nil, "echo test")
	model.ready = true
	model.terminalWidth = 80
	model.terminalHeight = 24

	viewStr := model.View()
	if !strings.Contains(viewStr, "runtop:") {
		t.Errorf("Expected TUI view to contain header with 'runtop:', but got:\n%q", viewStr)
	}
}

type EightCPUsCollector struct{}

func (e *EightCPUsCollector) Collect() (TelemetryData, error) {
	return TelemetryData{
		CPUs:     []float64{10, 20, 30, 40, 50, 60, 70, 80},
		RAM:      45.0,
		RAMTotal: 16 * 1024 * 1024 * 1024,
		RAMUsed:  16 * 1024 * 1024 * 1024 * 45 / 100,
		Swap:     5.0,
		GPUs:     []float64{15.0},
		Disks: []DiskUsage{
			{Mountpoint: "/", UsedPercent: 55.0, Total: 100 * 1024 * 1024 * 1024, Used: 55 * 1024 * 1024 * 1024},
			{Mountpoint: "/data", UsedPercent: 65.0, Total: 500 * 1024 * 1024 * 1024, Used: 325 * 1024 * 1024 * 1024},
		},
		Load1:  1.2,
		Load5:  0.8,
		Load15: 0.5,
		Uptime: 7200,
	}, nil
}

func Test8CPUsLayout(t *testing.T) {
	collector := &EightCPUsCollector{}
	model := NewModel(collector, nil, nil, "echo test")
	model.ready = true
	model.terminalWidth = 80
	model.terminalHeight = 24

	// Sync collect once to ensure telemetryData is updated
	data, _ := collector.Collect()
	model.telemetryData = data

	viewStr := model.View()
	cleanView := stripANSI(viewStr)

	// Check elements
	expectedElements := []string{
		"0 ",
		"7 ",
		"mem",
		"swap",
		"0",
		"/data",
		"uptime:",
		"load:",
		"7.2/16G",
		"55/100G",
		"325/500G",
	}
	for _, elem := range expectedElements {
		if !strings.Contains(cleanView, elem) {
			t.Errorf("Expected 8+ CPUs view to contain element %q, but got:\n%s", elem, cleanView)
		}
	}

	// Check for bracket styles around progress bars in htop mode
	if !strings.Contains(cleanView, "[") || !strings.Contains(cleanView, "]") {
		t.Errorf("Expected htop-style view to contain brackets '[' and ']' enclosing progress bars, but got:\n%s", cleanView)
	}

	// The title bar should not contain load and uptime in 8+ CPUs layout (redundant)
	if strings.Contains(cleanView, "system telemetry | load") {
		t.Errorf("Expected title bar not to contain redundant load/uptime, but got:\n%s", cleanView)
	}
}

func TestProcessTreeViewToggle(t *testing.T) {
	collector := &DummyCollector{}
	model := NewModel(collector, nil, nil, "echo test")

	if model.showProcessTree {
		t.Error("Expected showProcessTree to be initially false")
	}
	if model.activeWindow != 1 {
		t.Errorf("Expected activeWindow to be initially 1, got %d", model.activeWindow)
	}

	var m *Model = model

	// Toggle showProcessTree with 'm' key
	var rawModel tea.Model
	rawModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	m = rawModel.(*Model)
	if !m.showProcessTree {
		t.Error("Expected showProcessTree to be true after pressing m")
	}

	// Toggle back showProcessTree with 'M' key
	rawModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("M")})
	m = rawModel.(*Model)
	if m.showProcessTree {
		t.Error("Expected showProcessTree to be false after pressing M")
	}
}

func TestBuildTree(t *testing.T) {
	procMap := map[int]ProcessInfo{
		100: {Pid: 100, Ppid: 1, User: "g", CPU: 1.5, Mem: 2.0, Time: "00:05", Command: "parent-proc"},
		200: {Pid: 200, Ppid: 100, User: "g", CPU: 0.5, Mem: 1.0, Time: "00:01", Command: "child-1"},
		300: {Pid: 300, Ppid: 100, User: "g", CPU: 0.1, Mem: 0.5, Time: "00:00", Command: "child-2"},
		400: {Pid: 400, Ppid: 300, User: "g", CPU: 0.0, Mem: 0.1, Time: "00:00", Command: "grandchild"},
	}

	children := map[int][]int{
		1:   {100},
		100: {200, 300},
		300: {400},
	}

	var result []string
	buildTree(100, 0, []bool{true}, children, procMap, &result)

	expected := []string{
		"100 g 1.5 2.0 00:05 parent-proc",
		"200 g 0.5 1.0 00:01 ├─ child-1",
		"300 g 0.1 0.5 00:00 └─ child-2",
		"400 g 0.0 0.1 00:00    └─ grandchild",
	}

	if len(result) != len(expected) {
		t.Fatalf("Expected %d lines, got %d", len(expected), len(result))
	}

	for i, line := range result {
		cleanLine := strings.Join(strings.Fields(line), " ")
		cleanExpected := strings.Join(strings.Fields(expected[i]), " ")
		if cleanLine != cleanExpected {
			t.Errorf("At line %d: expected %q, got %q", i, cleanExpected, cleanLine)
		}
	}
}

func TestProcessTreeViewLayout(t *testing.T) {
	collector := &DummyCollector{}
	model := NewModel(collector, nil, nil, "echo test")
	model.ready = true
	model.terminalWidth = 80
	model.terminalHeight = 24

	// Toggle view to show process tree
	model.showProcessTree = true
	model.processTreeLines = []string{
		"   100 g          1.5   2.0    00:05 parent-proc",
		"   200 g          0.5   1.0    00:01 ├─ child-1",
	}

	viewStr := model.View()
	cleanView := stripANSI(viewStr)

	// Check sections
	if !strings.Contains(cleanView, "process tree") {
		t.Errorf("Expected view to contain section 'process tree', but got:\n%s", cleanView)
	}

	// Check table headers
	expectedHeaders := []string{"pid", "user", "cpu%", "mem%", "time", "command"}
	for _, header := range expectedHeaders {
		if !strings.Contains(cleanView, header) {
			t.Errorf("Expected process tree header to contain %q, but got:\n%s", header, cleanView)
		}
	}

	// Check process tree lines
	expectedProcs := []string{"parent-proc", "child-1", "100", "200"}
	for _, proc := range expectedProcs {
		if !strings.Contains(cleanView, proc) {
			t.Errorf("Expected process tree view to contain %q, but got:\n%s", proc, cleanView)
		}
	}
}

func TestConfirmQuit(t *testing.T) {
	collector := &DummyCollector{}
	model := NewModel(collector, nil, nil, "echo test")

	// Trigger ready state
	model.ready = true
	model.terminalWidth = 80
	model.terminalHeight = 24

	// Initially confirmQuit should be false
	if model.confirmQuit {
		t.Error("Expected confirmQuit to be initially false")
	}

	// Pressing "q" should trigger confirmQuit
	var rawModel tea.Model
	var cmd tea.Cmd
	rawModel, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	m := rawModel.(*Model)
	if !m.confirmQuit {
		t.Error("Expected confirmQuit to be true after pressing 'q'")
	}
	if cmd != nil {
		t.Error("Expected no cmd (e.g. tea.Quit) immediately on pressing 'q'")
	}

	// Verify the view changes to show confirmation prompt
	viewStr := m.View()
	cleanView := stripANSI(viewStr)
	if !strings.Contains(cleanView, "Are you sure you want to quit? (y/N)") {
		t.Errorf("Expected view to show quit confirmation prompt, got:\n%s", cleanView)
	}
	if !strings.Contains(cleanView, "Press [y] to confirm quit, or [n] to cancel") {
		t.Errorf("Expected view to show updated help bar, got:\n%s", cleanView)
	}

	// Pressing "n" should cancel confirmQuit
	rawModel, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = rawModel.(*Model)
	if m.confirmQuit {
		t.Error("Expected confirmQuit to be false after pressing 'n'")
	}

	// Pressing "q" again to re-trigger
	rawModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	m = rawModel.(*Model)
	if !m.confirmQuit {
		t.Error("Expected confirmQuit to be true again after pressing 'q'")
	}

	// Pressing "y" requests supervised shutdown; exit is deferred until Wait completes.
	rawModel, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if cmd == nil {
		t.Fatal("Expected shutdown command, got nil")
	}
	if msg := cmd(); msg != nil {
		t.Errorf("Expected shutdown command to complete without a message, got %T", msg)
	}
	rawModel, cmd = rawModel.(*Model).Update(SubprocessExitMsg{StartTime: time.Now(), EndTime: time.Now()})
	if cmd == nil {
		t.Fatal("Expected tea.Quit after supervised process exit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("Expected tea.Quit after process exit")
	}

	// Pressing ctrl+c also requests supervised shutdown.
	model2 := NewModel(collector, nil, nil, "echo test")
	rawModel, cmd = model2.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("Expected shutdown command on ctrl+c, got nil")
	}
	if msg := cmd(); msg != nil {
		t.Errorf("Expected ctrl+c shutdown command to return nil, got %T", msg)
	}
}

func TestSmallTerminalView(t *testing.T) {
	model := NewModel(&DummyCollector{}, nil, nil, "echo a very long command")
	model.ready = true
	model.terminalWidth = 12
	model.terminalHeight = 3

	view := model.View()
	lines := strings.Split(view, "\n")
	if len(lines) > model.terminalHeight {
		t.Fatalf("view has %d rows for a %d-row terminal", len(lines), model.terminalHeight)
	}
	for _, line := range lines {
		if lipgloss.Width(line) > model.terminalWidth {
			t.Errorf("view line width %d exceeds terminal width %d: %q", lipgloss.Width(line), model.terminalWidth, line)
		}
	}
}

func TestUniformGraphWidths(t *testing.T) {
	collector := &EightCPUsCollector{}
	data, _ := collector.Collect()
	data.Disks = []DiskUsage{
		{Mountpoint: "/", UsedPercent: 10, Total: 100 * 1024 * 1024 * 1024, Used: 10 * 1024 * 1024 * 1024},
		{Mountpoint: "/short", UsedPercent: 20, Total: 200 * 1024 * 1024 * 1024, Used: 40 * 1024 * 1024 * 1024},
		{Mountpoint: "/very/long/mountpoint/that/needs/truncation", UsedPercent: 30, Total: 300 * 1024 * 1024 * 1024, Used: 90 * 1024 * 1024 * 1024},
	}
	data.Target = ProcessTelemetry{PID: 42, Processes: 3, CPUPercent: 125, RSSBytes: 512 * 1024 * 1024}

	for _, width := range []int{40, 60, 80, 120, 160} {
		t.Run(fmt.Sprintf("width_%d", width), func(t *testing.T) {
			model := NewModel(collector, nil, nil, "echo test")
			model.telemetryData = data
			innerWidth := width - 4
			panel := stripANSI(model.renderTelemetryPanel(
				innerWidth,
				lipgloss.Color("#50FA7B"),
				lipgloss.Color("#8BE9FD"),
				lipgloss.Color("#FFB86C"),
				lipgloss.Color("#FF5555"),
				lipgloss.Color("#6272A4"),
				lipgloss.Color("#D9D9D9"),
			))
			layout := newMetricGridLayout(innerWidth, len(data.CPUs))
			graphCount := 0
			for _, line := range strings.Split(panel, "\n") {
				if lipgloss.Width(line) > innerWidth {
					t.Errorf("line width %d exceeds panel width %d: %q", lipgloss.Width(line), innerWidth, line)
				}
				offset := 0
				for {
					start := strings.Index(line[offset:], "[")
					if start < 0 {
						break
					}
					start += offset
					end := strings.Index(line[start:], "]")
					if end < 0 {
						t.Fatalf("unterminated graph in %q", line)
					}
					end += start
					if got := end - start - 1; got != layout.barWidth {
						t.Errorf("graph width %d, want %d in %q", got, layout.barWidth, line)
					}
					column := graphCountOnLine(line[:start])
					expectedStart := metricLabelWidth + 1 + column*(layout.cellWidth+metricColumnGap)
					if start != expectedStart {
						t.Errorf("graph starts at %d, want %d in %q", start, expectedStart, line)
					}
					graphCount++
					offset = end + 1
				}
			}
			if graphCount < len(data.CPUs)+len(data.GPUs)+len(data.Disks)+4 {
				t.Errorf("found only %d metric graphs", graphCount)
			}
			if !strings.Contains(panel, "/very/l...") {
				t.Error("long filesystem label was not uniformly truncated")
			}
		})
	}
}

func graphCountOnLine(prefix string) int {
	return strings.Count(prefix, "]")
}
