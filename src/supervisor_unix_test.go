//go:build unix

package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSupervisorTerminatesProcessGroup(t *testing.T) {
	supervisor := NewProcessSupervisor("sleep 30 & echo $!; wait", nil)
	if _, ok := supervisor.start().(SubprocessStartedMsg); !ok {
		t.Fatal("expected subprocess to start")
	}

	var childPID int
	select {
	case event := <-supervisor.events:
		batch, ok := event.(OutputBatchMsg)
		if !ok || len(batch.Lines) == 0 {
			t.Fatalf("expected child PID output, got %#v", event)
		}
		var err error
		childPID, err = strconv.Atoi(strings.TrimSpace(batch.Lines[0]))
		if err != nil {
			t.Fatalf("parse child PID: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for child PID")
	}

	supervisor.stop(200 * time.Millisecond)
	select {
	case event := <-supervisor.events:
		if _, ok := event.(SubprocessExitMsg); !ok {
			t.Fatalf("expected exit event after stop, got %#v", event)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for process-group exit")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", childPID))
		if os.IsNotExist(err) {
			return
		}
		if err == nil {
			fields := strings.Fields(string(state))
			if len(fields) > 2 && fields[2] == "Z" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child process %d remained alive after group termination", childPID)
}
