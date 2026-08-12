package main

import (
	"strings"
	"testing"
	"time"
)

func TestSupervisorDrainsOutputBeforeExit(t *testing.T) {
	const lineCount = 500
	supervisor := NewProcessSupervisor("i=0; while [ $i -lt 500 ]; do echo line-$i; i=$((i+1)); done", nil)
	started, ok := supervisor.start().(SubprocessStartedMsg)
	if !ok {
		t.Fatal("expected subprocess to start")
	}
	if started.PID <= 0 {
		t.Fatalf("invalid subprocess PID %d", started.PID)
	}

	seen := 0
	for {
		select {
		case event := <-supervisor.events:
			switch msg := event.(type) {
			case OutputBatchMsg:
				seen += len(msg.Lines)
			case SubprocessExitMsg:
				if msg.Err != nil || msg.StreamErr != nil {
					t.Fatalf("subprocess failed: process=%v stream=%v", msg.Err, msg.StreamErr)
				}
				if seen != lineCount {
					t.Fatalf("exit arrived after %d lines; expected %d", seen, lineCount)
				}
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for subprocess exit")
		}
	}
}

func TestSupervisorHandlesLongLinesAndSanitizesDisplay(t *testing.T) {
	longLine := "\x1b[31m" + strings.Repeat("x", 100<<10) + "\x1b[0m"
	var commandLog strings.Builder
	supervisor := NewProcessSupervisor("true", &commandLog)

	if err := supervisor.scanOutput(strings.NewReader(longLine + "\n")); err != nil {
		t.Fatalf("scanOutput failed: %v", err)
	}
	event := <-supervisor.events
	batch, ok := event.(OutputBatchMsg)
	if !ok || len(batch.Lines) != 1 {
		t.Fatalf("expected one output line, got %#v", event)
	}
	if strings.Contains(batch.Lines[0], "\x1b[") {
		t.Fatal("display output retained ANSI control sequences")
	}
	if len(batch.Lines[0]) != 100<<10 {
		t.Fatalf("long line length changed: got %d", len(batch.Lines[0]))
	}
	if !strings.Contains(commandLog.String(), "\x1b[31m") {
		t.Fatal("raw command log did not retain the original ANSI stream")
	}
}

func TestTelemetrySamplingIsSingleFlight(t *testing.T) {
	model := NewModel(&DummyCollector{}, nil, nil, "true")
	model.sampling = true

	_, cmd := model.Update(TickMsg(time.Now()))
	if cmd == nil {
		t.Fatal("tick command should always be rescheduled")
	}
	if !model.sampling {
		t.Fatal("in-flight sampling flag was cleared by another tick")
	}

	_, _ = model.Update(TelemetryResultMsg{Data: TelemetryData{CPUs: []float64{1}}})
	if model.sampling {
		t.Fatal("sampling flag was not cleared after a result")
	}
}
