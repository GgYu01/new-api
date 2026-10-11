package logger

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
)

func TestGetSetMaxLogSizeMB(t *testing.T) {
	old := GetMaxLogSizeMB()
	defer SetMaxLogSizeMB(old)

	SetMaxLogSizeMB(50)
	if got := GetMaxLogSizeMB(); got != 50 {
		t.Fatalf("expected 50, got %d", got)
	}

	// Negative or zero defaults to 100
	SetMaxLogSizeMB(0)
	if got := GetMaxLogSizeMB(); got != 100 {
		t.Fatalf("expected 100, got %d", got)
	}

	SetMaxLogSizeMB(-5)
	if got := GetMaxLogSizeMB(); got != 100 {
		t.Fatalf("expected 100, got %d", got)
	}
}

func TestPruneLogFiles(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "newapi-log-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	origLogDir := common.LogDir
	common.LogDir = &tempDir
	defer func() { common.LogDir = origLogDir }()

	origLimit := GetMaxLogSizeMB()
	defer SetMaxLogSizeMB(origLimit)

	// Create 4 simulated log files with different modification times
	// file1: 1MB, oldest
	// file2: 1MB
	// file3: 1MB
	// file4: 1MB, newest (active)
	fileNames := []string{
		"oneapi-20261011010000.log",
		"oneapi-20261011020000.log",
		"oneapi-20261011030000.log",
		"oneapi-20261011040000.log",
	}

	baseTime := time.Now().Add(-1 * time.Hour)
	payload := make([]byte, 1024*1024) // 1MB

	for i, name := range fileNames {
		p := filepath.Join(tempDir, name)
		if err := os.WriteFile(p, payload, 0644); err != nil {
			t.Fatalf("failed to write dummy log: %v", err)
		}
		modTime := baseTime.Add(time.Duration(i*10) * time.Minute)
		_ = os.Chtimes(p, modTime, modTime)
	}

	// Mark file4 as current active log
	activeLogPath := filepath.Join(tempDir, fileNames[3])
	currentLogPathMu.Lock()
	currentLogPath = activeLogPath
	currentLogPathMu.Unlock()

	// Total size is 4MB. Set limit to 2MB.
	// PruneLogFiles should remove file1 and file2 (oldest), keeping file3 and active file4.
	maxLogSizeMB.Store(2)

	deleted, freed, err := PruneLogFiles()
	if err != nil {
		t.Fatalf("PruneLogFiles failed: %v", err)
	}

	if deleted != 2 {
		t.Errorf("expected 2 deleted files, got %d", deleted)
	}
	if freed != 2*1024*1024 {
		t.Errorf("expected 2MB freed, got %d", freed)
	}

	// Verify file1 and file2 are gone
	if _, err := os.Stat(filepath.Join(tempDir, fileNames[0])); !os.IsNotExist(err) {
		t.Errorf("file1 should have been deleted")
	}
	if _, err := os.Stat(filepath.Join(tempDir, fileNames[1])); !os.IsNotExist(err) {
		t.Errorf("file2 should have been deleted")
	}

	// Verify file3 and file4 still exist
	if _, err := os.Stat(filepath.Join(tempDir, fileNames[2])); err != nil {
		t.Errorf("file3 should still exist: %v", err)
	}
	if _, err := os.Stat(activeLogPath); err != nil {
		t.Errorf("active log file4 must NEVER be deleted: %v", err)
	}
}
