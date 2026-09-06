package zapslog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap/exp/zapslog"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// New creates a Zap-backed slog logger and an idempotent cleanup function from options.
// Call cleanup after the logger is no longer in use.
func New(opts ...Option) (*slog.Logger, func(), error) {
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}
	return NewLogger(cfg)
}

// NewLogger creates a Zap-backed slog logger and an idempotent cleanup function from a complete configuration.
// Call cleanup after the logger is no longer in use.
func NewLogger(cfg *Log) (*slog.Logger, func(), error) {
	if cfg == nil {
		cfg = defaultConfig()
	}
	cores, files, err := buildCores(cfg)
	if err != nil {
		return nil, nil, err
	}
	opts := []zapslog.HandlerOption{
		zapslog.WithCaller(true),
		zapslog.AddStacktraceAt(slog.LevelError + 1),
	}
	if cfg.AppName != "" {
		opts = append(opts, zapslog.WithName(cfg.AppName))
	}
	cleanup := sync.OnceFunc(func() {
		for _, file := range files {
			_ = file.Close()
		}
	})
	return slog.New(zapslog.NewHandler(zapcore.NewTee(cores...), opts...)), cleanup, nil
}

// OmitAttrs returns a slog logger that removes the named top-level attributes.
func OmitAttrs(logger *slog.Logger, keys ...string) *slog.Logger {
	omitted := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		omitted[key] = struct{}{}
	}
	return slog.New(omitHandler{Handler: logger.Handler(), omitted: omitted})
}

type omitHandler struct {
	slog.Handler
	omitted map[string]struct{}
}

func (h omitHandler) Handle(ctx context.Context, record slog.Record) error {
	filtered := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		if _, omitted := h.omitted[attr.Key]; !omitted {
			filtered.AddAttrs(attr)
		}
		return true
	})
	return h.Handler.Handle(ctx, filtered)
}

func (h omitHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	filtered := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		if _, omitted := h.omitted[attr.Key]; !omitted {
			filtered = append(filtered, attr)
		}
	}
	return omitHandler{Handler: h.Handler.WithAttrs(filtered), omitted: h.omitted}
}

func (h omitHandler) WithGroup(name string) slog.Handler {
	return omitHandler{Handler: h.Handler.WithGroup(name), omitted: h.omitted}
}

func buildCores(cfg *Log) ([]zapcore.Core, []*lumberjack.Logger, error) {
	level, err := validateConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	enc, err := newEncoder(cfg.Format, cfg.TimeFormat)
	if err != nil {
		return nil, nil, err
	}
	coreLevel := zapLevel(level)
	console := os.Stdout
	if cfg.Output == "stderr" {
		console = os.Stderr
	}
	cores := []zapcore.Core{zapcore.NewCore(enc, zapcore.Lock(console), coreLevel)}
	if !cfg.File {
		return cores, nil, nil
	}
	filename := cfg.Output
	if filename == "stdout" || filename == "stderr" {
		filename = "app.log"
		if cfg.AppName != "" {
			filename = cfg.AppName + ".log"
		}
	}
	filePath := filepath.Join(cfg.Dir, filename)
	file := fileWriter(cfg, filePath)
	files := []*lumberjack.Logger{file}
	cores = append(cores, zapcore.NewCore(enc.Clone(), zapcore.AddSync(file), coreLevel))
	if cfg.ErrorFile {
		ext := filepath.Ext(filePath)
		errorPath := strings.TrimSuffix(filePath, ext) + "_error" + ext
		errorFile := fileWriter(cfg, errorPath)
		files = append(files, errorFile)
		cores = append(cores, zapcore.NewCore(enc.Clone(), zapcore.AddSync(errorFile), zapcore.ErrorLevel))
	}
	return cores, files, nil
}

func validateConfig(cfg *Log) (slog.Level, error) {
	var level slog.Level
	if cfg.Level != "" {
		if err := level.UnmarshalText([]byte(cfg.Level)); err != nil {
			return 0, fmt.Errorf("zapslog: invalid level %q: %w", cfg.Level, err)
		}
	}
	if err := cfg.ValidateAll(); err != nil {
		return 0, fmt.Errorf("zapslog: invalid config: %w", err)
	}
	if cfg.ErrorFile && !cfg.File {
		return 0, errors.New("zapslog: error file requires file output")
	}
	if cfg.File && cfg.Dir == "" {
		return 0, errors.New("zapslog: file directory is required")
	}
	return level, nil
}

func newEncoder(format, timeFormat string) (zapcore.Encoder, error) {
	cfg := zapcore.EncoderConfig{
		TimeKey:       "T",
		LevelKey:      "L",
		NameKey:       "N",
		CallerKey:     "C",
		FunctionKey:   zapcore.OmitKey,
		MessageKey:    "M",
		StacktraceKey: "S",
		LineEnding:    zapcore.DefaultLineEnding,
		EncodeTime: func(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
			enc.AppendString(t.Format(timeFormat))
		},
		ConsoleSeparator: " ",
	}
	switch format {
	case "json":
		cfg.EncodeLevel = zapcore.CapitalLevelEncoder
		cfg.EncodeName = zapcore.FullNameEncoder
		cfg.EncodeCaller = zapcore.ShortCallerEncoder
		return zapcore.NewJSONEncoder(cfg), nil
	case formatConsole:
		cfg.EncodeLevel = zapcore.CapitalColorLevelEncoder
		cfg.EncodeName = func(name string, encoder zapcore.PrimitiveArrayEncoder) {
			encoder.AppendString("[" + name + "]")
		}
		cfg.EncodeCaller = zapcore.FullCallerEncoder
		return zapcore.NewConsoleEncoder(cfg), nil
	default:
		return nil, fmt.Errorf("zapslog: invalid format %q", format)
	}
}

func fileWriter(cfg *Log, filename string) *lumberjack.Logger {
	return &lumberjack.Logger{
		Filename:   filename,
		MaxSize:    int(cfg.MaxSize),
		MaxBackups: int(cfg.MaxBackups),
		MaxAge:     int(cfg.MaxAge),
		Compress:   cfg.Compress,
		LocalTime:  true,
	}
}

func zapLevel(level slog.Level) zapcore.Level {
	switch {
	case level < slog.LevelInfo:
		return zapcore.DebugLevel
	case level < slog.LevelWarn:
		return zapcore.InfoLevel
	case level < slog.LevelError:
		return zapcore.WarnLevel
	default:
		return zapcore.ErrorLevel
	}
}
