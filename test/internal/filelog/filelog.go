package filelog

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	timeFormat        = "2006/01/02 15:04:05.000"
	defaultMaxSize    = 10
	defaultMaxAge     = 7
	defaultMaxBackups = 3
)

var ErrClosed = errors.New("file logger is closed")

type Log struct {
	mu       sync.RWMutex
	logger   *log.Logger
	close    func() error
	closeErr error
	closed   bool
}

func New(path string) (*Log, error) {
	path, err := preparePath(path)
	if err != nil {
		return nil, err
	}
	output := &lumberjack.Logger{
		Filename:   path,
		MaxSize:    defaultMaxSize,
		MaxAge:     defaultMaxAge,
		MaxBackups: defaultMaxBackups,
		LocalTime:  true,
		Compress:   true,
	}
	return &Log{logger: log.New(output, "", 0), close: output.Close}, nil
}

func preparePath(path string) (string, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." {
		return "", errors.New("filelog: path is empty")
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("filelog: refusing symlink %q", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("filelog: inspect path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", fmt.Errorf("filelog: create directory: %w", err)
	}
	return path, nil
}

func (logger *Log) Write(format string, args ...any) error {
	logger.mu.RLock()
	defer logger.mu.RUnlock()
	if logger.closed {
		return ErrClosed
	}
	message := fmt.Sprintf(format, args...)
	return logger.logger.Output(2, fmt.Sprintf("[%s] %s", time.Now().Format(timeFormat), message))
}

func (logger *Log) Close() error {
	logger.mu.Lock()
	defer logger.mu.Unlock()
	if !logger.closed {
		logger.closed = true
		logger.closeErr = logger.close()
	}
	return logger.closeErr
}
