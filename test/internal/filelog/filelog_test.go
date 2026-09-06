package filelog

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestLogWritesFormattedText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "table.log")
	logger, err := New(path)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if writeErr := logger.Write("[进入房间] 玩家:%d", 7); writeErr != nil {
		t.Fatalf("Write() error = %v", writeErr)
	}
	if closeErr := logger.Close(); closeErr != nil {
		t.Fatalf("Close() error = %v", closeErr)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	want := `^\[\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}\.\d{3}\] \[进入房间\] 玩家:7\n$`
	if !regexp.MustCompile(want).Match(data) {
		t.Fatalf("log data = %q", data)
	}
	if err := logger.Write("after close"); !errors.Is(err, ErrClosed) {
		t.Fatalf("Write() after Close error = %v, want %v", err, ErrClosed)
	}
}

func TestLogReportsRuntimeWriteFailure(t *testing.T) {
	want := errors.New("write failed")
	logger := &Log{logger: log.New(errorWriter{err: want}, "", 0)}

	if err := logger.Write("cannot write"); !errors.Is(err, want) {
		t.Fatalf("Write() error = %v, want %v", err, want)
	}
}

func TestLogCloseRetainsError(t *testing.T) {
	want := errors.New("close failed")
	calls := 0
	logger := &Log{close: func() error {
		calls++
		return want
	}}

	for attempt := 1; attempt <= 2; attempt++ {
		if err := logger.Close(); !errors.Is(err, want) {
			t.Fatalf("Close() attempt %d error = %v, want %v", attempt, err, want)
		}
	}
	if calls != 1 {
		t.Fatalf("close calls = %d, want 1", calls)
	}
}

type errorWriter struct{ err error }

func (writer errorWriter) Write([]byte) (int, error) { return 0, writer.err }
