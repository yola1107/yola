package zapslog_test

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"log/slog"
	"math"
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

	mainLog, err := fs.ReadFile(os.DirFS(dir), "yola.log")
	require.NoError(t, err)
	errorLog, err := fs.ReadFile(os.DirFS(dir), "yola_error.log")
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
	fileOutput, err := fs.ReadFile(os.DirFS(dir), "gateway.log")
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
	for _, field := range []struct {
		name   string
		option func(int) zapslog.Option
		get    func(*zapslog.Log) int32
	}{
		{"size", zapslog.WithMaxSize, (*zapslog.Log).GetMaxSize},
		{"backups", zapslog.WithMaxBackups, (*zapslog.Log).GetMaxBackups},
		{"age", zapslog.WithMaxAge, (*zapslog.Log).GetMaxAge},
	} {
		t.Run(field.name, func(t *testing.T) {
			cfg := &zapslog.Log{MaxSize: 1, MaxBackups: 1, MaxAge: 1}
			field.option(-1)(cfg)
			require.Equal(t, int32(1), field.get(cfg), "negative limit must keep the configured value")
			if math.MaxInt > math.MaxInt32 {
				field.option(math.MaxInt)(cfg)
				require.Equal(t, int32(1), field.get(cfg), "oversized limit must keep the configured value")
			}
			field.option(0)(cfg)
			require.Zero(t, field.get(cfg), "zero is a valid limit")
		})
	}
}

func TestLoggerRespectsSlogLevels(t *testing.T) {
	dir := t.TempDir()
	logger, cleanup, err := zapslog.New(
		zapslog.WithFormat("json"),
		zapslog.WithOutput("yola.log"),
		zapslog.WithDir(dir),
		zapslog.WithFile(true),
		zapslog.WithLevel("warn"),
	)
	require.NoError(t, err)

	logger.Info("filtered")
	logger.Log(context.Background(), slog.LevelWarn, "written")
	cleanup()

	output, err := fs.ReadFile(os.DirFS(dir), "yola.log")
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
