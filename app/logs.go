package able

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/natefinch/lumberjack"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	defaultLogFile       = "logs/app.log"
	defaultDedupInterval = 10 * time.Second

	contextStartTimeKey = "time"
	contextTraceIDKey   = "trace_id"
)

var contextFieldKeys = [...]string{
	"request",
	"response",
	"context",
	"category",
	"ip",
	"type",
	"sub_type",
	contextTraceIDKey,
}

// Log is the package-level default logger.
var Log = NewFromEnv()

// Config defines the logger runtime configuration.
type Config struct {
	Level         zapcore.Level
	Console       bool
	File          bool
	Filename      string
	MaxSizeMB     int
	MaxBackups    int
	MaxAgeDays    int
	Compress      bool
	DedupInterval time.Duration
}

// Option customizes Config without expanding constructor parameters.
type Option func(*Config)

type SugaredLogger struct {
	*zap.SugaredLogger

	cacheMu       sync.Mutex
	logCache      map[string]logEntry
	dedupInterval time.Duration
}

type logEntry struct {
	lastLogged time.Time
	count      int
}

// DefaultConfig returns the production-oriented default logger configuration.
func DefaultConfig() Config {
	return Config{
		Level:         zapcore.InfoLevel,
		Console:       true,
		File:          true,
		Filename:      defaultLogFile,
		MaxSizeMB:     50,
		MaxBackups:    7,
		MaxAgeDays:    30,
		Compress:      true,
		DedupInterval: defaultDedupInterval,
	}
}

func NewFromEnv() *SugaredLogger {
	cfg := DefaultConfig()
	cfg.Level = logLevelFromEnv()
	cfg.Console = logBoolFromEnv("LOG_CONSOLE", cfg.Console)
	cfg.File = logBoolFromEnv("LOG_FILE", cfg.File)
	cfg.Filename = logStringFromEnv("LOG_FILE_PATH", cfg.Filename)
	cfg.DedupInterval = logDurationFromEnv("LOG_DEDUP_INTERVAL", cfg.DedupInterval)

	return New(WithConfig(cfg))
}

func New(opts ...Option) *SugaredLogger {
	cfg := DefaultConfig()
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	cfg.normalize()

	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.EncodeTime = zapcore.RFC3339TimeEncoder
	encoder := zapcore.NewJSONEncoder(encoderConfig)

	cores := make([]zapcore.Core, 0, 2)
	if cfg.File {
		fileWriter := zapcore.AddSync(&lumberjack.Logger{
			Filename:   cfg.Filename,
			MaxSize:    cfg.MaxSizeMB,
			MaxBackups: cfg.MaxBackups,
			MaxAge:     cfg.MaxAgeDays,
			Compress:   cfg.Compress,
		})
		cores = append(cores, zapcore.NewCore(encoder, fileWriter, cfg.Level))
	}
	if cfg.Console {
		cores = append(cores, zapcore.NewCore(encoder, zapcore.AddSync(os.Stdout), cfg.Level))
	}

	core := zapcore.NewNopCore()
	if len(cores) > 0 {
		core = zapcore.NewTee(cores...)
	}

	logger := zap.New(core, zap.AddCaller())
	return &SugaredLogger{
		SugaredLogger: logger.Sugar(),
		logCache:      make(map[string]logEntry),
		dedupInterval: cfg.DedupInterval,
	}
}

func WithConfig(cfg Config) Option {
	return func(target *Config) {
		*target = cfg
	}
}

func WithLevel(level zapcore.Level) Option {
	return func(cfg *Config) {
		cfg.Level = level
	}
}

func WithConsole(enabled bool) Option {
	return func(cfg *Config) {
		cfg.Console = enabled
	}
}

func WithFile(filename string) Option {
	return func(cfg *Config) {
		cfg.File = true
		cfg.Filename = filename
	}
}

func WithDedupInterval(interval time.Duration) Option {
	return func(cfg *Config) {
		cfg.DedupInterval = interval
	}
}

func (c *Config) normalize() {
	if strings.TrimSpace(c.Filename) == "" {
		c.Filename = defaultLogFile
	}
	if c.MaxSizeMB <= 0 {
		c.MaxSizeMB = 50
	}
	if c.MaxBackups < 0 {
		c.MaxBackups = 0
	}
	if c.MaxAgeDays < 0 {
		c.MaxAgeDays = 0
	}
	if c.DedupInterval <= 0 {
		c.DedupInterval = defaultDedupInterval
	}
}

func logLevelFromEnv() zapcore.Level {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL"))) {
	case "debug":
		return zapcore.DebugLevel
	case "info":
		return zapcore.InfoLevel
	case "warn", "warning":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	}

	switch strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV"))) {
	case "develop", "development", "dev", "local":
		return zapcore.DebugLevel
	case "production", "prod":
		return zapcore.WarnLevel
	default:
		return zapcore.InfoLevel
	}
}

func logBoolFromEnv(name string, defaultValue bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "t", "true", "y", "yes", "on":
		return true
	case "0", "f", "false", "n", "no", "off":
		return false
	default:
		return defaultValue
	}
}

func logStringFromEnv(name, defaultValue string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return defaultValue
}

func logDurationFromEnv(name string, defaultValue time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return defaultValue
	}

	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return defaultValue
	}
	return duration
}

// WithContext returns a logger enriched with stable fields from ctx.
func (s *SugaredLogger) WithContext(ctx context.Context) *zap.SugaredLogger {
	if s == nil || s.SugaredLogger == nil {
		return zap.NewNop().Sugar()
	}
	if ctx == nil {
		ctx = context.Background()
	}

	now := time.Now()
	startedAt := contextTime(ctx, contextStartTimeKey, now)
	traceID := contextString(ctx, contextTraceIDKey)
	if traceID == "" {
		traceID = newTraceID()
	}

	fields := make([]interface{}, 0, 2+len(contextFieldKeys))
	fields = append(fields,
		zap.Int64("timeline", now.UnixNano()),
		zap.Duration("duration", now.Sub(startedAt)),
	)

	for _, key := range contextFieldKeys {
		value := ctx.Value(key)
		if value == nil && key == contextTraceIDKey {
			value = traceID
		}
		if value != nil {
			fields = append(fields, zap.Any(key, value))
		}
	}

	return s.With(fields...)
}

// InitContext returns a derived context with start time and trace_id.
func InitContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Value(contextStartTimeKey) == nil {
		ctx = context.WithValue(ctx, contextStartTimeKey, time.Now())
	}
	if contextString(ctx, contextTraceIDKey) == "" {
		ctx = context.WithValue(ctx, contextTraceIDKey, newTraceID())
	}
	return ctx
}

// LogWithDeduplication suppresses repeated messages in a fixed time window.
func (s *SugaredLogger) LogWithDeduplication(msg string) {
	if s == nil || s.SugaredLogger == nil {
		return
	}

	now := time.Now()
	suppressed := 0
	shouldLog := false

	s.cacheMu.Lock()
	if s.logCache == nil {
		s.logCache = make(map[string]logEntry)
	}
	entry, exists := s.logCache[msg]
	if exists && now.Sub(entry.lastLogged) < s.dedupInterval {
		entry.count++
		s.logCache[msg] = entry
	} else {
		if exists {
			suppressed = entry.count
		}
		s.logCache[msg] = logEntry{lastLogged: now}
		shouldLog = true
	}
	s.cacheMu.Unlock()

	if suppressed > 0 {
		s.Infow("duplicate log messages suppressed", "message", msg, "count", suppressed)
	}
	if shouldLog {
		s.Info(msg)
	}
}

func contextTime(ctx context.Context, key string, defaultValue time.Time) time.Time {
	value, ok := ctx.Value(key).(time.Time)
	if !ok || value.IsZero() {
		return defaultValue
	}
	return value
}

func contextString(ctx context.Context, key string) string {
	switch value := ctx.Value(key).(type) {
	case string:
		return value
	case interface{ String() string }:
		return value.String()
	default:
		return ""
	}
}

func newTraceID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return time.Now().UTC().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(buf[:])
}

func (s *SugaredLogger) Sync() error {
	if s == nil || s.SugaredLogger == nil {
		return nil
	}

	err := s.SugaredLogger.Sync()
	if errors.Is(err, os.ErrInvalid) || errors.Is(err, syscall.EINVAL) {
		return nil
	}
	return err
}
