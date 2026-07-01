package main

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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
		{percent: 0, width: 10, expected: "----------"},
		{percent: 100, width: 10, expected: "##########"},
		{percent: 50, width: 10, expected: "#####-----"},
		{percent: -10, width: 5, expected: "-----"},
		{percent: 120, width: 5, expected: "#####"},
	}

	for _, tt := range tests {
		rawOutput := drawProgressBar(tt.percent, tt.width, nil)
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
	collector := &DummyCollector{}
	var buf strings.Builder
	model := NewModel(collector, nil, &buf, "echo test")

	model.appendLog("test line 1")
	model.appendLog("test line 2")

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
