package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Version is injected at build time using ldflags (defined in Makefile).
var Version = "0.0.1-dev"

type Config struct {
	Version bool
	Command string
}

func ParseArgs(args []string) (Config, error) {
	var cfg Config

	for i := 1; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--version":
			cfg.Version = true
		default:
			if strings.HasPrefix(arg, "-") {
				return Config{}, fmt.Errorf("unknown flag: %s", arg)
			}
			if cfg.Command != "" {
				return Config{}, fmt.Errorf("multiple commands specified")
			}
			cfg.Command = arg
		}
	}

	return cfg, nil
}

func main() {
	cfg, err := ParseArgs(os.Args)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		printUsage(Version)
		os.Exit(1)
	}

	if cfg.Version {
		fmt.Printf("runtop version: %s\n", Version)
		return
	}

	if cfg.Command == "" {
		fmt.Printf("Error: no command specified\n")
		printUsage(Version)
		os.Exit(1)
	}

	// Initialize the system telemetry collector.
	collector := NewSystemCollector()

	// Initialize logs.
	var logger *MultiCSVLogger
	var cmdWriter io.WriteCloser

	logPath := os.Getenv("RUNTOP_LOGPATH") // e.g. "./logs"
	if logPath == "" {
		logPath = "./logs/"
	}

	// Ensure the log directory exists
	if err := os.MkdirAll(logPath, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: failed to create log directory: %v\n", err)
		os.Exit(1)
	}

	timestamp := time.Now().Format("20060102150405")
	logger = NewMultiCSVLogger(logPath, timestamp)

	cmdLog := filepath.Join(logPath, fmt.Sprintf("runtop-%s.log", timestamp))
	f, err := os.OpenFile(cmdLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: failed to open command log file: %v\n", err)
		os.Exit(1)
	}
	cmdWriter = f
	defer cmdWriter.Close()

	// Initialize the main TUI model.
	model := NewModel(collector, logger, cmdWriter, cfg.Command)

	// Run bubbletea program with the alternative screen buffer active to allow full window layouts.
	program := tea.NewProgram(model, tea.WithAltScreen())

	m, err := program.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: runtop failed to run: %v\n", err)
		os.Exit(1)
	}

	finalModel := m.(*Model)

	// Write summary metadata text file
	metaLog := filepath.Join(logPath, fmt.Sprintf("runtop-%s.txt", timestamp))
	if err := writeMetaLog(metaLog, finalModel); err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: failed to write runtop summary metadata file: %v\n", err)
	}

	if finalModel.exitErr != nil {
		if exitError, ok := finalModel.exitErr.(*exec.ExitError); ok {
			os.Exit(exitError.ExitCode())
		}
		os.Exit(1)
	}
}

func writeMetaLog(filePath string, model *Model) error {
	startSec := model.startTime.Unix()
	startNsec := model.startTime.Nanosecond()
	endSec := model.endTime.Unix()
	endNsec := model.endTime.Nanosecond()

	returnCode := 0
	if model.exitErr != nil {
		if exitError, ok := model.exitErr.(*exec.ExitError); ok {
			returnCode = exitError.ExitCode()
		} else {
			returnCode = 1
		}
	}

	runtimeSec := float64(model.endTime.UnixNano()-model.startTime.UnixNano()) / 1e9

	content := fmt.Sprintf(
		"start(s.n) %d.%09d\nreturn(code) %d\nend(s.n) %d.%09d\nruntime(sec) %.9f\n",
		startSec, startNsec,
		returnCode,
		endSec, endNsec,
		runtimeSec,
	)

	return os.WriteFile(filePath, []byte(content), 0644)
}

func printUsage(version string) {
	fmt.Printf("Usage: runtop (%s) [command]\n", version)
	fmt.Println("  runtop [command]       # Execute a command")
	fmt.Println("  runtop --version       # Show version")
}
