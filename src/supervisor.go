package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	ansi "github.com/charmbracelet/x/ansi"
)

const (
	outputBatchSize = 64
	outputBatchWait = 16 * time.Millisecond
	maxOutputLine   = 4 << 20
)

type SubprocessStartedMsg struct {
	PID       int
	StartTime time.Time
}

type OutputBatchMsg struct {
	Lines []string
}

type SubprocessExitMsg struct {
	Err       error
	StreamErr error
	StartTime time.Time
	EndTime   time.Time
}

type ShutdownRequestMsg struct{}

// ProcessSupervisor is the sole owner of exec.Cmd and its lifecycle.
type ProcessSupervisor struct {
	command string
	writer  io.Writer
	events  chan tea.Msg
	done    chan struct{}

	mu            sync.Mutex
	cmd           *exec.Cmd
	pid           int
	started       bool
	stopRequested bool
	stopOnce      sync.Once
}

func NewProcessSupervisor(command string, writer io.Writer) *ProcessSupervisor {
	return &ProcessSupervisor{
		command: command,
		writer:  writer,
		events:  make(chan tea.Msg, 128),
		done:    make(chan struct{}),
	}
}

func (s *ProcessSupervisor) StartCmd() tea.Cmd {
	return func() tea.Msg { return s.start() }
}

func (s *ProcessSupervisor) ListenCmd() tea.Cmd {
	return func() tea.Msg { return <-s.events }
}

func (s *ProcessSupervisor) StopCmd() tea.Cmd {
	return func() tea.Msg {
		s.stop(2 * time.Second)
		return nil
	}
}

func (s *ProcessSupervisor) start() tea.Msg {
	startTime := time.Now()
	cmd := exec.Command("sh", "-c", s.command)
	configureProcess(cmd)

	reader, writer := io.Pipe()
	cmd.Stdout = writer
	cmd.Stderr = writer

	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return SubprocessExitMsg{Err: errors.New("subprocess already started"), StartTime: startTime, EndTime: time.Now()}
	}
	s.started = true
	s.cmd = cmd
	stopRequested := s.stopRequested
	s.mu.Unlock()

	if stopRequested {
		_ = reader.Close()
		_ = writer.Close()
		close(s.done)
		return SubprocessExitMsg{Err: errors.New("subprocess start canceled"), StartTime: startTime, EndTime: time.Now()}
	}

	if err := cmd.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		close(s.done)
		return SubprocessExitMsg{Err: err, StartTime: startTime, EndTime: time.Now()}
	}

	s.mu.Lock()
	s.pid = cmd.Process.Pid
	s.mu.Unlock()

	go s.wait(cmd, reader, writer, startTime)
	return SubprocessStartedMsg{PID: cmd.Process.Pid, StartTime: startTime}
}

func (s *ProcessSupervisor) wait(cmd *exec.Cmd, reader *io.PipeReader, writer *io.PipeWriter, startTime time.Time) {
	scanDone := make(chan error, 1)
	go func() { scanDone <- s.scanOutput(reader) }()

	waitErr := cmd.Wait()
	_ = writer.Close()
	scanErr := <-scanDone
	_ = reader.Close()
	endTime := time.Now()
	close(s.done)
	s.events <- SubprocessExitMsg{
		Err:       waitErr,
		StreamErr: scanErr,
		StartTime: startTime,
		EndTime:   endTime,
	}
}

func (s *ProcessSupervisor) stop(grace time.Duration) {
	s.mu.Lock()
	s.stopRequested = true
	pid := s.pid
	started := s.started
	done := s.done
	s.mu.Unlock()

	if !started || pid == 0 {
		return
	}

	select {
	case <-done:
		return
	default:
	}

	s.stopOnce.Do(func() { _ = signalProcessTree(pid, false) })
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-done:
		return
	case <-timer.C:
		_ = signalProcessTree(pid, true)
	}
	<-done
}

func (s *ProcessSupervisor) scanOutput(reader io.Reader) error {
	lines := make(chan string, outputBatchSize)
	errCh := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64<<10), maxOutputLine)
		scanner.Split(splitTerminalLines)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		errCh <- scanner.Err()
		close(lines)
	}()

	batchTimer := time.NewTimer(outputBatchWait)
	if !batchTimer.Stop() {
		<-batchTimer.C
	}
	defer batchTimer.Stop()
	var batchTimerC <-chan time.Time
	writerTicker := time.NewTicker(time.Second)
	defer writerTicker.Stop()
	batch := make([]string, 0, outputBatchSize)
	var writeErr error
	lastWriterFlush := time.Now()
	flush := func(forceWriterFlush bool) {
		if len(batch) > 0 {
			out := append([]string(nil), batch...)
			s.events <- OutputBatchMsg{Lines: out}
			batch = batch[:0]
		}
		if flusher, ok := s.writer.(interface{ Flush() error }); ok && (forceWriterFlush || time.Since(lastWriterFlush) >= time.Second) {
			if err := flusher.Flush(); err != nil {
				writeErr = errors.Join(writeErr, fmt.Errorf("flush command log: %w", err))
			}
			lastWriterFlush = time.Now()
		}
	}

	for {
		select {
		case line, ok := <-lines:
			if !ok {
				flush(true)
				return errors.Join(<-errCh, writeErr)
			}
			if s.writer != nil {
				if _, err := fmt.Fprintln(s.writer, line); err != nil {
					writeErr = errors.Join(writeErr, fmt.Errorf("write command log: %w", err))
				}
			}
			batch = append(batch, sanitizeTerminalLine(line))
			if len(batch) == 1 {
				batchTimer.Reset(outputBatchWait)
				batchTimerC = batchTimer.C
			}
			if len(batch) == outputBatchSize {
				flush(false)
				if !batchTimer.Stop() {
					select {
					case <-batchTimer.C:
					default:
					}
				}
				batchTimerC = nil
			}
		case <-batchTimerC:
			flush(false)
			batchTimerC = nil
		case <-writerTicker.C:
			flush(true)
		}
	}
}

func sanitizeTerminalLine(line string) string {
	line = ansi.Strip(line)
	return strings.Map(func(r rune) rune {
		if r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, line)
}

// splitTerminalLines treats carriage-return progress updates as viewport lines.
func splitTerminalLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i, b := range data {
		if b == '\n' || b == '\r' {
			advance = i + 1
			if b == '\r' && advance < len(data) && data[advance] == '\n' {
				advance++
			}
			return advance, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}
