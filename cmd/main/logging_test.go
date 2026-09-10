package main

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigureSlogWritesInfoJSONAndFiltersDebug(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })

	path := filepath.Join(t.TempDir(), "nested", "laxcode.log")
	f, err := configureSlog(path)
	if err != nil {
		t.Fatal(err)
	}
	slog.Debug("must_not_be_written")
	slog.Info("context_compaction_test", "input_tokens", 123)
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	logFile, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	scanner := bufio.NewScanner(logFile)
	if !scanner.Scan() {
		t.Fatal("missing info log record")
	}
	var record map[string]any
	if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
		t.Fatalf("log is not JSON: %v", err)
	}
	if record["level"] != "INFO" || record["msg"] != "context_compaction_test" || record["input_tokens"] != float64(123) {
		t.Fatalf("unexpected log record: %+v", record)
	}
	if scanner.Scan() {
		t.Fatalf("debug log should be filtered: %s", scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}
