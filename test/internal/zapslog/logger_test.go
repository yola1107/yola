package zapslog_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"yola/test/internal/zapslog"

	"github.com/stretchr/testify/require"
)

func TestOmitAttrs(t *testing.T) {
	var output bytes.Buffer
	logger := zapslog.OmitAttrs(slog.New(slog.NewJSONHandler(&output, nil)), "request")

	logger.With(
		"request", "stored-secret",
		"service", "game",
	).Error(
		"panic recovered",
		"request", "direct-secret",
		"error", "synthetic panic",
	)

	require.Contains(t, output.String(), `"service":"game"`)
	require.Contains(t, output.String(), `"error":"synthetic panic"`)
	require.NotContains(t, output.String(), "stored-secret")
	require.NotContains(t, output.String(), "direct-secret")
}

func TestLoggerWritesStructuredFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "log")
	restoreStdout := captureStdout(t)
	logger, cleanup, err := zapslog.New(
		zapslog.WithFormat("json"),
		zapslog.WithOutput("yola.log"),
		zapslog.WithDir(dir),
		zapslog.WithAppName("gateway"),
		zapslog.WithFile(true),
		zapslog.WithErrorFile(true),
	)
	require.NoError(t, err)

	logger.WithGroup("request").Info("request received", slog.String("key", "player-a"))
	logger.Error("request failed", slog.String("error", "synthetic error"))
	consoleOutput := restoreStdout()
	cleanup()
	cleanup()

	mainLog, err := os.ReadFile(filepath.Join(dir, "yola.log"))
	require.NoError(t, err)
	errorLog, err := os.ReadFile(filepath.Join(dir, "yola_error.log"))
	require.NoError(t, err)
	require.Contains(t, string(mainLog), `"N":"gateway"`)
	require.Contains(t, string(mainLog), "logger_test.go")
	require.Contains(t, string(mainLog), `"request":{"key":"player-a"}`)
	require.Contains(t, string(mainLog), `"M":"request failed"`)
	require.Contains(t, consoleOutput, `"M":"request failed"`)
	require.NotContains(t, string(errorLog), "request received")
	require.Contains(t, string(errorLog), `"M":"request failed"`)
	require.NotContains(t, string(errorLog), `"S":`)
}

func TestWithFileWritesConsoleAndLocalFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "log")
	restoreStdout := captureStdout(t)
	logger, cleanup, err := zapslog.New(
		zapslog.WithFormat("json"),
		zapslog.WithTimeFormat("2006"),
		zapslog.WithOutput("stdout"),
		zapslog.WithDir(dir),
		zapslog.WithAppName("gateway"),
		zapslog.WithFile(true),
	)
	require.NoError(t, err)
	logger.Info("combined output")
	consoleOutput := restoreStdout()
	cleanup()
	require.Contains(t, consoleOutput, `"M":"combined output"`)
	fileOutput, err := os.ReadFile(filepath.Join(dir, "gateway.log"))
	require.NoError(t, err)
	require.Contains(t, string(fileOutput), `"M":"combined output"`)
	require.Regexp(t, `"T":"\d{4}"`, string(fileOutput))
}

func TestConsoleWrapsLoggerNameInBrackets(t *testing.T) {
	restoreStdout := captureStdout(t)
	logger, cleanup, err := zapslog.New(
		zapslog.WithFormat("console"),
		zapslog.WithAppName("gateway"),
	)
	require.NoError(t, err)
	logger.Info("started")
	output := restoreStdout()
	cleanup()
	require.Contains(t, output, "[gateway]")
}

func TestWithFileDisabledWritesConsoleOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "log")
	restoreStdout := captureStdout(t)
	logger, cleanup, err := zapslog.New(
		zapslog.WithFormat("json"),
		zapslog.WithOutput("yola.log"),
		zapslog.WithDir(dir),
		zapslog.WithFile(false),
	)
	require.NoError(t, err)
	logger.Info("console only")
	require.Contains(t, restoreStdout(), `"M":"console only"`)
	cleanup()
	_, statErr := os.Stat(dir)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestRotationOptions(t *testing.T) {
	tests := []struct {
		name   string
		option zapslog.Option
		value  func(*zapslog.Log) int32
		want   int32
	}{
		{name: "zero max size", option: zapslog.WithMaxSize(0), value: func(cfg *zapslog.Log) int32 { return cfg.MaxSize }, want: 0},
		{name: "zero max backups", option: zapslog.WithMaxBackups(0), value: func(cfg *zapslog.Log) int32 { return cfg.MaxBackups }, want: 0},
		{name: "zero max age", option: zapslog.WithMaxAge(0), value: func(cfg *zapslog.Log) int32 { return cfg.MaxAge }, want: 0},
		{name: "negative max size", option: zapslog.WithMaxSize(-1), value: func(cfg *zapslog.Log) int32 { return cfg.MaxSize }, want: 1},
		{name: "negative max backups", option: zapslog.WithMaxBackups(-1), value: func(cfg *zapslog.Log) int32 { return cfg.MaxBackups }, want: 1},
		{name: "negative max age", option: zapslog.WithMaxAge(-1), value: func(cfg *zapslog.Log) int32 { return cfg.MaxAge }, want: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := &zapslog.Log{MaxSize: 1, MaxBackups: 1, MaxAge: 1}
			test.option(cfg)
			if got := test.value(cfg); got != test.want {
				t.Errorf("configured value = %d, want %d", got, test.want)
			}
		})
	}
}

func TestLoggerRespectsSlogLevels(t *testing.T) {
	dir := t.TempDir()
	level := slog.LevelWarn
	logger, cleanup, err := zapslog.New(
		zapslog.WithFormat("json"),
		zapslog.WithOutput("yola.log"),
		zapslog.WithDir(dir),
		zapslog.WithFile(true),
		zapslog.WithLevel("warn"),
	)
	require.NoError(t, err)

	logger.Info("filtered")
	logger.Log(context.Background(), level, "written")
	cleanup()

	output, err := os.ReadFile(filepath.Join(dir, "yola.log"))
	require.NoError(t, err)
	require.NotContains(t, string(output), "filtered")
	require.Contains(t, string(output), `"M":"written"`)
}

func TestWithLevelRejectsInvalidValue(t *testing.T) {
	logger, cleanup, err := zapslog.New(zapslog.WithLevel("invalid"))

	require.Nil(t, logger)
	require.Nil(t, cleanup)
	require.ErrorContains(t, err, `invalid level "invalid"`)
}

func captureStdout(t *testing.T) func() string {
	t.Helper()
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	original := os.Stdout
	os.Stdout = writer
	restored := false
	t.Cleanup(func() {
		if !restored {
			os.Stdout = original
			_ = writer.Close()
		}
		_ = reader.Close()
	})
	return func() string {
		t.Helper()
		os.Stdout = original
		restored = true
		require.NoError(t, writer.Close())
		output, readErr := io.ReadAll(reader)
		require.NoError(t, readErr)
		return string(output)
	}
}

func TestLoggerRejectsInvalidConfiguration(t *testing.T) {
	for name, test := range map[string]struct {
		cfg *zapslog.Log
		err string
	}{
		"format": {cfg: &zapslog.Log{Format: "text", Output: "stdout"}, err: "Log.Format"},
		"output": {cfg: &zapslog.Log{Format: "console"}, err: "Log.Output"},
		"error file": {
			cfg: &zapslog.Log{Format: "console", Output: "stdout", ErrorFile: true},
			err: "zapslog: error file requires file output",
		},
		"file directory": {
			cfg: &zapslog.Log{Format: "json", Output: "yola.log", File: true},
			err: "zapslog: file directory is required",
		},
		"negative max size": {
			cfg: &zapslog.Log{Format: "console", Output: "stdout", Dir: ".", File: true, MaxSize: -1},
			err: "Log.MaxSize",
		},
		"negative max backups": {
			cfg: &zapslog.Log{Format: "console", Output: "stdout", Dir: ".", File: true, MaxBackups: -2},
			err: "Log.MaxBackups",
		},
		"negative max age": {
			cfg: &zapslog.Log{Format: "console", Output: "stdout", Dir: ".", File: true, MaxAge: -3},
			err: "Log.MaxAge",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := zapslog.NewLogger(test.cfg)
			require.ErrorContains(t, err, test.err)
		})
	}
}
