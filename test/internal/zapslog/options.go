package zapslog

const formatConsole = "console"

// defaultConfig 返回默认配置
func defaultConfig() *Log {
	return &Log{
		Level:      "debug",
		Format:     formatConsole,
		TimeFormat: "2006/01/02 15:04:05.000",
		Output:     "stdout",
		Dir:        "./log",
		AppName:    "app",
		MaxSize:    100,
		MaxBackups: 7,
		MaxAge:     7,
		Compress:   true,
		File:       false,
		ErrorFile:  false,
	}
}

// Option 配置选项
type Option func(*Log)

// WithLevel 设置日志级别，解析和校验由 New 完成。
func WithLevel(level string) Option {
	return func(c *Log) { c.Level = level }
}

// WithFormat 设置输出格式
func WithFormat(format string) Option {
	return func(c *Log) { c.Format = format }
}

// WithTimeFormat 设置时间格式，使用 Go time layout。
// 如：zapslog.WithTimeFormat("2006-01-02T15:04:05.000Z07:00")
func WithTimeFormat(format string) Option {
	return func(c *Log) { c.TimeFormat = format }
}

// WithOutput 设置控制台目标或本地日志文件名
func WithOutput(output string) Option {
	return func(c *Log) { c.Output = output }
}

// WithDir 设置日志目录
func WithDir(dir string) Option {
	return func(c *Log) { c.Dir = dir }
}

// WithAppName 设置应用名称
func WithAppName(name string) Option {
	return func(c *Log) { c.AppName = name }
}

// WithMaxSize 设置单文件最大大小（MB）
func WithMaxSize(size int) Option {
	return func(c *Log) {
		if size >= 0 {
			c.MaxSize = int32(size)
		}
	}
}

// WithMaxBackups 设置保留文件数
func WithMaxBackups(count int) Option {
	return func(c *Log) {
		if count >= 0 {
			c.MaxBackups = int32(count)
		}
	}
}

// WithMaxAge 设置保留天数
func WithMaxAge(days int) Option {
	return func(c *Log) {
		if days >= 0 {
			c.MaxAge = int32(days)
		}
	}
}

// WithCompress 设置是否压缩
func WithCompress(compress bool) Option {
	return func(c *Log) { c.Compress = compress }
}

// WithFile 设置是否在控制台输出之外同时写本地文件
func WithFile(enabled bool) Option {
	return func(c *Log) { c.File = enabled }
}

// WithErrorFile 设置是否额外写错误级别日志文件，启用前需先启用 File。
func WithErrorFile(enabled bool) Option {
	return func(c *Log) { c.ErrorFile = enabled }
}
