package main

import (
	"encoding/csv"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
)

// TelemetryData holds the system telemetry metrics.
type TelemetryData struct {
	Timestamp time.Time
	CPUs      []float64 // Percentage (0-100) per logical CPU
	RAM       float64   // Percentage (0-100)
	GPUs      []float64 // Percentage (0-100) per GPU
	Disk      float64   // Percentage (0-100)
}

// TelemetryCollector defines a clean Go interface for collecting system metrics.
type TelemetryCollector interface {
	Collect() (TelemetryData, error)
}

// SystemCollector implements TelemetryCollector using gopsutil and mock GPU fallbacks.
type SystemCollector struct {
	randSource *rand.Rand
}

// NewSystemCollector creates a new SystemCollector.
func NewSystemCollector() *SystemCollector {
	return &SystemCollector{
		randSource: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Collect gathers CPUs, RAM, and Disk metrics using gopsutil, and mocks GPU metrics.
func (s *SystemCollector) Collect() (TelemetryData, error) {
	data := TelemetryData{
		Timestamp: time.Now(),
	}

	// CPUs utilization (per logical CPU core)
	cpuPercents, err := cpu.Percent(0, true)
	if err == nil && len(cpuPercents) > 0 {
		data.CPUs = cpuPercents
	} else {
		// Mock CPU utilization (4 logical cores) if gopsutil fails or runs in a restricted environment
		data.CPUs = make([]float64, 4)
		for i := 0; i < 4; i++ {
			data.CPUs[i] = 5.0 + s.randSource.Float64()*20.0
		}
	}

	// RAM utilization
	vMem, err := mem.VirtualMemory()
	if err == nil {
		data.RAM = vMem.UsedPercent
	} else {
		// Mock RAM utilization
		data.RAM = 35.0 + s.randSource.Float64()*15.0
	}

	// Disk utilization
	dUsage, err := disk.Usage("/")
	if err == nil {
		data.Disk = dUsage.UsedPercent
	} else {
		// Mock Disk utilization
		data.Disk = 45.0 + s.randSource.Float64()*5.0
	}

	// GPUs utilization (Query real GPU utilization via nvidia-smi)
	if gpus, err := getNvidiaGPUUtilization(); err == nil {
		data.GPUs = gpus
	} else {
		data.GPUs = []float64{}
	}

	return data, nil
}

// MultiCSVLogger handles logging system metrics to split CSV files.
type MultiCSVLogger struct {
	logDir    string
	timestamp string
	mu        sync.Mutex
}

// NewMultiCSVLogger initializes a new MultiCSVLogger.
func NewMultiCSVLogger(logDir string, timestamp string) *MultiCSVLogger {
	return &MultiCSVLogger{
		logDir:    logDir,
		timestamp: timestamp,
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

// Log logs all telemetry data points to their respective split CSV files.
func (l *MultiCSVLogger) Log(data TelemetryData) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	// 1. Log CPU
	cpuHeader := make([]string, len(data.CPUs)+1)
	cpuHeader[0] = "timestamp"
	cpuRow := make([]string, len(data.CPUs)+1)
	cpuRow[0] = data.Timestamp.Format(time.RFC3339)
	for i, val := range data.CPUs {
		cpuHeader[i+1] = fmt.Sprintf("cpu%d", i)
		cpuRow[i+1] = fmt.Sprintf("%.2f", val)
	}
	if err := l.writeRow(l.CPUPath(), cpuHeader, cpuRow); err != nil {
		return err
	}

	// 2. Log GPU
	gpuHeader := make([]string, len(data.GPUs)+1)
	gpuHeader[0] = "timestamp"
	gpuRow := make([]string, len(data.GPUs)+1)
	gpuRow[0] = data.Timestamp.Format(time.RFC3339)
	for i, val := range data.GPUs {
		gpuHeader[i+1] = fmt.Sprintf("gpu%d", i)
		gpuRow[i+1] = fmt.Sprintf("%.2f", val)
	}
	if err := l.writeRow(l.GPUPath(), gpuHeader, gpuRow); err != nil {
		return err
	}

	// 3. Log RAM
	ramHeader := []string{"timestamp", "ram"}
	ramRow := []string{
		data.Timestamp.Format(time.RFC3339),
		fmt.Sprintf("%.2f", data.RAM),
	}
	if err := l.writeRow(l.RAMPath(), ramHeader, ramRow); err != nil {
		return err
	}

	// 4. Log Disk
	diskHeader := []string{"timestamp", "disk"}
	diskRow := []string{
		data.Timestamp.Format(time.RFC3339),
		fmt.Sprintf("%.2f", data.Disk),
	}
	if err := l.writeRow(l.DiskPath(), diskHeader, diskRow); err != nil {
		return err
	}

	return nil
}

// writeRow helper writes a header (if file is new) and a row to a CSV file.
func (l *MultiCSVLogger) writeRow(filePath string, header []string, row []string) error {
	fileExisted := true
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		fileExisted = false
	}

	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open file %s: %w", filePath, err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	if !fileExisted {
		if err := writer.Write(header); err != nil {
			return fmt.Errorf("failed to write CSV header: %w", err)
		}
	}

	if err := writer.Write(row); err != nil {
		return fmt.Errorf("failed to write CSV row: %w", err)
	}

	return nil
}

// getNvidiaGPUUtilization queries the utilization of each Nvidia GPU in the system.
func getNvidiaGPUUtilization() ([]float64, error) {
	cmd := exec.Command("nvidia-smi", "--query-gpu=utilization.gpu", "--format=csv,noheader,nounits")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var utils []float64
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var val float64
		if _, err := fmt.Sscanf(line, "%f", &val); err == nil {
			utils = append(utils, val)
		}
	}
	return utils, nil
}
