package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/process"
)

type DiskUsage struct {
	Device      string
	Mountpoint  string
	UsedPercent float64
	Total       uint64
	Used        uint64
}

// TelemetryData holds the system telemetry metrics.
type TelemetryData struct {
	Timestamp time.Time
	CPUs      []float64   // Percentage (0-100) per logical CPU
	RAM       float64     // Percentage (0-100)
	RAMTotal  uint64      // Bytes
	RAMUsed   uint64      // Bytes
	Swap      float64     // Percentage (0-100)
	GPUs      []float64   // Percentage (0-100) per GPU
	Disk      float64     // Legacy singular disk usage (from "/" or first mount)
	DiskTotal uint64      // Bytes
	DiskUsed  uint64      // Bytes
	Disks     []DiskUsage // Mountpoint + UsedPercent for all physical mounted drives
	Load1     float64
	Load5     float64
	Load15    float64
	Uptime    uint64 // Seconds
	Target    ProcessTelemetry
}

// ProcessTelemetry contains aggregate metrics for the supervised process tree.
type ProcessTelemetry struct {
	PID         int32
	Processes   int
	CPUPercent  float64
	RSSBytes    uint64
	ReadBytes   uint64
	WriteBytes  uint64
	CPUTimeSecs float64
}

// TelemetryCollector defines a clean Go interface for collecting system metrics.
type TelemetryCollector interface {
	Collect() (TelemetryData, error)
}

type TargetPIDCollector interface {
	SetTargetPID(pid int32)
}

// SystemCollector gathers host telemetry and aggregate metrics for a target process tree.
type SystemCollector struct {
	mu              sync.Mutex
	targetPID       int32
	previousCPUTime float64
	previousSample  time.Time
}

// NewSystemCollector creates a new SystemCollector.
func NewSystemCollector() *SystemCollector {
	return &SystemCollector{}
}

func (s *SystemCollector) SetTargetPID(pid int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.targetPID = pid
	s.previousCPUTime = 0
	s.previousSample = time.Time{}
}

// Collect gathers host telemetry and, when configured, target process-tree metrics.
func (s *SystemCollector) Collect() (TelemetryData, error) {
	data := TelemetryData{
		Timestamp: time.Now(),
	}
	var collectionErrors []error

	// CPUs utilization (per logical CPU core)
	cpuPercents, err := cpu.Percent(0, true)
	if err == nil && len(cpuPercents) > 0 {
		data.CPUs = cpuPercents
	} else {
		if err == nil {
			err = errors.New("no logical CPUs returned")
		}
		collectionErrors = append(collectionErrors, fmt.Errorf("collect CPU utilization: %w", err))
	}

	// RAM utilization
	vMem, err := mem.VirtualMemory()
	if err == nil {
		data.RAM = vMem.UsedPercent
		data.RAMTotal = vMem.Total
		data.RAMUsed = vMem.Used
	} else {
		collectionErrors = append(collectionErrors, fmt.Errorf("collect memory utilization: %w", err))
	}

	// Swap utilization
	sMem, err := mem.SwapMemory()
	if err == nil {
		data.Swap = sMem.UsedPercent
	} else {
		data.Swap = 0.0
		collectionErrors = append(collectionErrors, fmt.Errorf("collect swap utilization: %w", err))
	}

	// Load Average
	if avg, err := load.Avg(); err == nil {
		data.Load1 = avg.Load1
		data.Load5 = avg.Load5
		data.Load15 = avg.Load15
	} else {
		collectionErrors = append(collectionErrors, fmt.Errorf("collect load average: %w", err))
	}

	// Uptime
	if uptime, err := host.Uptime(); err == nil {
		data.Uptime = uptime
	} else {
		collectionErrors = append(collectionErrors, fmt.Errorf("collect uptime: %w", err))
	}

	// Mounted drives (physical partitions)
	var disks []DiskUsage
	partitions, err := disk.Partitions(false)
	if err == nil {
		for _, part := range partitions {
			if strings.HasPrefix(part.Device, "/dev/loop") || strings.HasPrefix(part.Mountpoint, "/boot/") {
				continue
			}
			if strings.Contains(part.Mountpoint, "/snap") || strings.Contains(part.Mountpoint, "/var/lib/snapd") {
				continue
			}
			if part.Fstype == "squashfs" || part.Fstype == "tmpfs" {
				continue
			}
			dUsage, err := disk.Usage(part.Mountpoint)
			if err == nil {
				disks = append(disks, DiskUsage{
					Device:      part.Device,
					Mountpoint:  part.Mountpoint,
					UsedPercent: dUsage.UsedPercent,
					Total:       dUsage.Total,
					Used:        dUsage.Used,
				})
			} else {
				collectionErrors = append(collectionErrors, fmt.Errorf("collect disk usage for %s: %w", part.Mountpoint, err))
			}
		}
	} else {
		collectionErrors = append(collectionErrors, fmt.Errorf("list disk partitions: %w", err))
	}

	if len(disks) == 0 {
		// Fallback to /
		dUsage, err := disk.Usage("/")
		if err == nil {
			disks = append(disks, DiskUsage{
				Device:      "",
				Mountpoint:  "/",
				UsedPercent: dUsage.UsedPercent,
				Total:       dUsage.Total,
				Used:        dUsage.Used,
			})
			data.Disk = dUsage.UsedPercent
			data.DiskTotal = dUsage.Total
			data.DiskUsed = dUsage.Used
		} else {
			collectionErrors = append(collectionErrors, fmt.Errorf("collect disk usage for /: %w", err))
		}
	} else {
		data.Disk = disks[0].UsedPercent
		data.DiskTotal = disks[0].Total
		data.DiskUsed = disks[0].Used
		for _, d := range disks {
			if d.Mountpoint == "/" {
				data.Disk = d.UsedPercent
				data.DiskTotal = d.Total
				data.DiskUsed = d.Used
				break
			}
		}
	}
	sort.Slice(disks, func(i, j int) bool { return disks[i].Mountpoint < disks[j].Mountpoint })
	data.Disks = disks

	// GPUs utilization (Query real GPU utilization via nvidia-smi)
	if gpus, err := getNvidiaGPUUtilization(); err == nil {
		data.GPUs = gpus
	} else {
		data.GPUs = []float64{}
		if !errors.Is(err, exec.ErrNotFound) {
			collectionErrors = append(collectionErrors, fmt.Errorf("collect NVIDIA GPU utilization: %w", err))
		}
	}

	s.mu.Lock()
	targetPID := s.targetPID
	s.mu.Unlock()
	if targetPID > 0 {
		target, err := collectProcessTelemetry(targetPID)
		if err != nil {
			collectionErrors = append(collectionErrors, err)
		} else {
			s.mu.Lock()
			if !s.previousSample.IsZero() {
				elapsed := data.Timestamp.Sub(s.previousSample).Seconds()
				if elapsed > 0 && target.CPUTimeSecs >= s.previousCPUTime {
					target.CPUPercent = (target.CPUTimeSecs - s.previousCPUTime) / elapsed * 100
				}
			}
			s.previousCPUTime = target.CPUTimeSecs
			s.previousSample = data.Timestamp
			s.mu.Unlock()
			data.Target = target
		}
	}

	return data, errors.Join(collectionErrors...)
}

func collectProcessTelemetry(rootPID int32) (ProcessTelemetry, error) {
	processes, err := process.Processes()
	if err != nil {
		return ProcessTelemetry{}, fmt.Errorf("list target processes: %w", err)
	}

	byPID := make(map[int32]*process.Process, len(processes))
	children := make(map[int32][]int32)
	rootGroup, _ := processGroupID(int(rootPID))
	groupMembers := make([]int32, 0)
	for _, proc := range processes {
		ppid, err := proc.Ppid()
		if err != nil {
			continue
		}
		byPID[proc.Pid] = proc
		children[ppid] = append(children[ppid], proc.Pid)
		if group, err := processGroupID(int(proc.Pid)); err == nil && rootGroup > 0 && group == rootGroup {
			groupMembers = append(groupMembers, proc.Pid)
		}
	}
	if _, ok := byPID[rootPID]; !ok {
		return ProcessTelemetry{}, fmt.Errorf("target process %d is no longer visible", rootPID)
	}

	result := ProcessTelemetry{PID: rootPID}
	queue := append([]int32{rootPID}, groupMembers...)
	seen := make(map[int32]struct{})
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		proc, ok := byPID[pid]
		if !ok {
			continue
		}
		result.Processes++
		if times, err := proc.Times(); err == nil {
			result.CPUTimeSecs += times.User + times.System
		}
		if memory, err := proc.MemoryInfo(); err == nil {
			result.RSSBytes += memory.RSS
		}
		if counters, err := proc.IOCounters(); err == nil {
			result.ReadBytes += counters.ReadBytes
			result.WriteBytes += counters.WriteBytes
		}
		queue = append(queue, children[pid]...)
	}
	return result, nil
}

// MultiCSVLogger handles logging system metrics to split CSV files.
type MultiCSVLogger struct {
	logDir    string
	timestamp string
	mu        sync.Mutex
	sinks     map[string]*csvSink
	closed    bool
}

type csvSink struct {
	file   *os.File
	buffer *bufio.Writer
	writer *csv.Writer
	header []string
}

// NewMultiCSVLogger initializes a new MultiCSVLogger.
func NewMultiCSVLogger(logDir string, timestamp string) *MultiCSVLogger {
	return &MultiCSVLogger{
		logDir:    logDir,
		timestamp: timestamp,
		sinks:     make(map[string]*csvSink),
	}
}

// CPUPath returns the file path of the CPU logger.
func (l *MultiCSVLogger) CPUPath() string {
	return filepath.Join(l.logDir, fmt.Sprintf("runtop-%s-cpu.csv", l.timestamp))
}

// GPUPath returns the file path of the GPU logger.
func (l *MultiCSVLogger) GPUPath() string {
	return filepath.Join(l.logDir, fmt.Sprintf("runtop-%s-gpu.csv", l.timestamp))
}

// RAMPath returns the file path of the RAM logger.
func (l *MultiCSVLogger) RAMPath() string {
	return filepath.Join(l.logDir, fmt.Sprintf("runtop-%s-ram.csv", l.timestamp))
}

// DiskPath returns the file path of the Disk logger.
func (l *MultiCSVLogger) DiskPath() string {
	return filepath.Join(l.logDir, fmt.Sprintf("runtop-%s-disk.csv", l.timestamp))
}

func (l *MultiCSVLogger) ProcessPath() string {
	return filepath.Join(l.logDir, fmt.Sprintf("runtop-%s-process.csv", l.timestamp))
}

// Log logs all telemetry data points to their respective split CSV files.
func (l *MultiCSVLogger) Log(data TelemetryData) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errors.New("CSV logger is closed")
	}

	// 1. Log CPU (with load averages appended)
	cpuHeader := make([]string, len(data.CPUs)+4)
	cpuHeader[0] = "timestamp"
	for i := 0; i < len(data.CPUs); i++ {
		cpuHeader[i+1] = fmt.Sprintf("cpu%d", i)
	}
	cpuHeader[len(data.CPUs)+1] = "load1"
	cpuHeader[len(data.CPUs)+2] = "load5"
	cpuHeader[len(data.CPUs)+3] = "load15"

	cpuRow := make([]string, len(data.CPUs)+4)
	cpuRow[0] = data.Timestamp.Format(time.RFC3339Nano)
	for i, val := range data.CPUs {
		cpuRow[i+1] = fmt.Sprintf("%.2f", val)
	}
	cpuRow[len(data.CPUs)+1] = fmt.Sprintf("%.2f", data.Load1)
	cpuRow[len(data.CPUs)+2] = fmt.Sprintf("%.2f", data.Load5)
	cpuRow[len(data.CPUs)+3] = fmt.Sprintf("%.2f", data.Load15)

	if err := l.writeRow(l.CPUPath(), cpuHeader, cpuRow); err != nil {
		return err
	}

	// 2. Log GPU
	if len(data.GPUs) > 0 {
		gpuHeader := make([]string, len(data.GPUs)+1)
		gpuHeader[0] = "timestamp"
		gpuRow := make([]string, len(data.GPUs)+1)
		gpuRow[0] = data.Timestamp.Format(time.RFC3339Nano)
		for i, val := range data.GPUs {
			gpuHeader[i+1] = fmt.Sprintf("gpu%d", i)
			gpuRow[i+1] = fmt.Sprintf("%.2f", val)
		}
		if err := l.writeRow(l.GPUPath(), gpuHeader, gpuRow); err != nil {
			return err
		}
	}

	// 3. Log RAM (with swap appended)
	ramHeader := []string{"timestamp", "ram_percent", "ram_used_bytes", "ram_total_bytes", "swap_percent"}
	ramRow := []string{
		data.Timestamp.Format(time.RFC3339Nano),
		fmt.Sprintf("%.2f", data.RAM),
		fmt.Sprintf("%d", data.RAMUsed),
		fmt.Sprintf("%d", data.RAMTotal),
		fmt.Sprintf("%.2f", data.Swap),
	}
	if err := l.writeRow(l.RAMPath(), ramHeader, ramRow); err != nil {
		return err
	}

	// 4. Log filesystems in long form so mounts can appear or disappear safely.
	diskHeader := []string{"timestamp", "device", "mountpoint", "used_percent", "used_bytes", "total_bytes"}
	if len(data.Disks) == 0 {
		diskRow := []string{
			data.Timestamp.Format(time.RFC3339Nano),
			"",
			"/",
			fmt.Sprintf("%.2f", data.Disk),
			fmt.Sprintf("%d", data.DiskUsed),
			fmt.Sprintf("%d", data.DiskTotal),
		}
		if err := l.writeRow(l.DiskPath(), diskHeader, diskRow); err != nil {
			return err
		}
	} else {
		for _, d := range data.Disks {
			diskRow := []string{
				data.Timestamp.Format(time.RFC3339Nano),
				d.Device,
				d.Mountpoint,
				fmt.Sprintf("%.2f", d.UsedPercent),
				fmt.Sprintf("%d", d.Used),
				fmt.Sprintf("%d", d.Total),
			}
			if err := l.writeRow(l.DiskPath(), diskHeader, diskRow); err != nil {
				return err
			}
		}
	}

	if data.Target.PID > 0 {
		processHeader := []string{"timestamp", "pid", "processes", "cpu_percent", "cpu_time_seconds", "rss_bytes", "read_bytes", "write_bytes"}
		processRow := []string{
			data.Timestamp.Format(time.RFC3339Nano),
			fmt.Sprintf("%d", data.Target.PID),
			fmt.Sprintf("%d", data.Target.Processes),
			fmt.Sprintf("%.2f", data.Target.CPUPercent),
			fmt.Sprintf("%.6f", data.Target.CPUTimeSecs),
			fmt.Sprintf("%d", data.Target.RSSBytes),
			fmt.Sprintf("%d", data.Target.ReadBytes),
			fmt.Sprintf("%d", data.Target.WriteBytes),
		}
		if err := l.writeRow(l.ProcessPath(), processHeader, processRow); err != nil {
			return err
		}
	}

	return l.flushLocked()
}

// writeRow helper writes a header (if file is new) and a row to a CSV file.
func (l *MultiCSVLogger) writeRow(filePath string, header []string, row []string) error {
	sink, ok := l.sinks[filePath]
	if !ok {
		file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return fmt.Errorf("failed to open file %s: %w", filePath, err)
		}
		info, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return fmt.Errorf("stat CSV file %s: %w", filePath, err)
		}
		buffer := bufio.NewWriterSize(file, 64<<10)
		sink = &csvSink{file: file, buffer: buffer, writer: csv.NewWriter(buffer), header: append([]string(nil), header...)}
		l.sinks[filePath] = sink
		if info.Size() == 0 {
			if err := sink.writer.Write(header); err != nil {
				return fmt.Errorf("failed to write CSV header: %w", err)
			}
		}
	} else if !slices.Equal(sink.header, header) {
		return fmt.Errorf("CSV schema changed for %s", filePath)
	}

	if err := sink.writer.Write(row); err != nil {
		return fmt.Errorf("failed to write CSV row: %w", err)
	}
	return nil
}

func (l *MultiCSVLogger) flushLocked() error {
	var flushErrors []error
	for path, sink := range l.sinks {
		sink.writer.Flush()
		if err := sink.writer.Error(); err != nil {
			flushErrors = append(flushErrors, fmt.Errorf("flush CSV %s: %w", path, err))
			continue
		}
		if err := sink.buffer.Flush(); err != nil {
			flushErrors = append(flushErrors, fmt.Errorf("flush CSV buffer %s: %w", path, err))
		}
	}
	return errors.Join(flushErrors...)
}

func (l *MultiCSVLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true

	var closeErrors []error
	if err := l.flushLocked(); err != nil {
		closeErrors = append(closeErrors, err)
	}
	for path, sink := range l.sinks {
		if err := sink.file.Close(); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("close CSV %s: %w", path, err))
		}
	}
	return errors.Join(closeErrors...)
}

// getNvidiaGPUUtilization queries the utilization of each Nvidia GPU in the system.
func getNvidiaGPUUtilization() ([]float64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=utilization.gpu", "--format=csv,noheader,nounits")
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("nvidia-smi timed out: %w", ctx.Err())
		}
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			// nvidia-smi is commonly installed on systems with no active NVIDIA device.
			return []float64{}, nil
		}
		return nil, err
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var utils []float64
	invalidLines := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var val float64
		if _, err := fmt.Sscanf(line, "%f", &val); err == nil {
			utils = append(utils, val)
		} else {
			invalidLines++
		}
	}
	if invalidLines > 0 {
		return nil, fmt.Errorf("parse %d nvidia-smi output lines", invalidLines)
	}
	return utils, nil
}
