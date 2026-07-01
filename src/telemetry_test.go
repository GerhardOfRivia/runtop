package main

import (
	"encoding/csv"
	"os"
	"testing"
	"time"
)

func TestSystemCollectorCollect(t *testing.T) {
	collector := NewSystemCollector()
	data, err := collector.Collect()
	if err != nil {
		t.Fatalf("Expected no error from Collect(), got %v", err)
	}

	if data.Timestamp.IsZero() {
		t.Error("Expected non-zero timestamp")
	}

	if len(data.CPUs) == 0 {
		t.Error("Expected at least one CPU core collected")
	}
	for i, val := range data.CPUs {
		if val < 0.0 || val > 100.0 {
			t.Errorf("CPU core %d value out of range [0, 100]: %v", i, val)
		}
	}

	if data.RAM < 0.0 || data.RAM > 100.0 {
		t.Errorf("RAM value out of range [0, 100]: %v", data.RAM)
	}
	for i, val := range data.GPUs {
		if val < 0.0 || val > 100.0 {
			t.Errorf("GPU %d value out of range [0, 100]: %v", i, val)
		}
	}

	if data.Disk < 0.0 || data.Disk > 100.0 {
		t.Errorf("Disk value out of range [0, 100]: %v", data.Disk)
	}
}

func TestMultiCSVLogger(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "runtop-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	logger := NewMultiCSVLogger(tempDir, "20260701120000")

	data1 := TelemetryData{
		Timestamp: time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC),
		CPUs:      []float64{10.5, 20.0},
		RAM:       64.2,
		GPUs:      []float64{30.1, 40.2},
		Disk:      45.8,
	}

	if err := logger.Log(data1); err != nil {
		t.Fatalf("Log call failed: %v", err)
	}

	// Verify CPU split file
	cpuFile, err := os.Open(logger.CPUPath())
	if err != nil {
		t.Fatalf("Failed to open CPU file: %v", err)
	}
	defer cpuFile.Close()
	cpuReader := csv.NewReader(cpuFile)
	cpuRecords, _ := cpuReader.ReadAll()
	if len(cpuRecords) != 2 {
		t.Errorf("Expected 2 rows in CPU csv, got %d", len(cpuRecords))
	}
	if cpuRecords[0][1] != "cpu0" || cpuRecords[0][2] != "cpu1" {
		t.Errorf("Expected CPU header labels, got %v", cpuRecords[0])
	}
	if cpuRecords[1][1] != "10.50" || cpuRecords[1][2] != "20.00" {
		t.Errorf("Expected CPU values, got %v", cpuRecords[1])
	}

	// Verify GPU split file
	gpuFile, err := os.Open(logger.GPUPath())
	if err != nil {
		t.Fatalf("Failed to open GPU file: %v", err)
	}
	defer gpuFile.Close()
	gpuReader := csv.NewReader(gpuFile)
	gpuRecords, _ := gpuReader.ReadAll()
	if len(gpuRecords) != 2 {
		t.Errorf("Expected 2 rows in GPU csv, got %d", len(gpuRecords))
	}
	if gpuRecords[0][1] != "gpu0" || gpuRecords[0][2] != "gpu1" {
		t.Errorf("Expected GPU header labels, got %v", gpuRecords[0])
	}
	if gpuRecords[1][1] != "30.10" || gpuRecords[1][2] != "40.20" {
		t.Errorf("Expected GPU values, got %v", gpuRecords[1])
	}

	// Verify RAM split file
	ramFile, err := os.Open(logger.RAMPath())
	if err != nil {
		t.Fatalf("Failed to open RAM file: %v", err)
	}
	defer ramFile.Close()
	ramReader := csv.NewReader(ramFile)
	ramRecords, _ := ramReader.ReadAll()
	if ramRecords[0][1] != "ram" || ramRecords[1][1] != "64.20" {
		t.Errorf("Expected RAM values, got %v", ramRecords)
	}

	// Verify Disk split file
	diskFile, err := os.Open(logger.DiskPath())
	if err != nil {
		t.Fatalf("Failed to open Disk file: %v", err)
	}
	defer diskFile.Close()
	diskReader := csv.NewReader(diskFile)
	diskRecords, _ := diskReader.ReadAll()
	if diskRecords[0][1] != "disk" || diskRecords[1][1] != "45.80" {
		t.Errorf("Expected Disk values, got %v", diskRecords)
	}
}
