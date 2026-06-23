package able

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestInitContextAddsLogFields(t *testing.T) {
	ctx := InitContext(context.Background())

	if _, ok := ctx.Value(contextStartTimeKey).(time.Time); !ok {
		t.Fatalf("expected %q to be time.Time", contextStartTimeKey)
	}
	if got := contextString(ctx, contextTraceIDKey); got == "" {
		t.Fatalf("expected %q to be populated", contextTraceIDKey)
	}
}

func TestWithContextUsesSafeDefaults(t *testing.T) {
	core, recorded := observer.New(zapcore.InfoLevel)
	logger := &SugaredLogger{
		SugaredLogger: zap.New(core).Sugar(),
	}
	ctx := context.WithValue(context.Background(), contextStartTimeKey, "invalid")

	logger.WithContext(ctx).Info("request finished")

	entry := recorded.TakeAll()[0]
	if _, ok := entry.ContextMap()["duration"]; !ok {
		t.Fatal("expected duration field")
	}
	if _, ok := entry.ContextMap()[contextTraceIDKey]; !ok {
		t.Fatalf("expected %q field", contextTraceIDKey)
	}
}

func TestLogWithDeduplicationSuppressesRepeatedMessages(t *testing.T) {
	core, recorded := observer.New(zapcore.InfoLevel)
	logger := &SugaredLogger{
		SugaredLogger: zap.New(core).Sugar(),
		logCache:      make(map[string]logEntry),
		dedupInterval: time.Minute,
	}

	logger.LogWithDeduplication("database timeout")
	logger.LogWithDeduplication("database timeout")

	if got := recorded.Len(); got != 1 {
		t.Fatalf("expected only first message to be logged, got %d", got)
	}

	logger.cacheMu.Lock()
	entry := logger.logCache["database timeout"]
	entry.lastLogged = time.Now().Add(-2 * time.Minute)
	logger.logCache["database timeout"] = entry
	logger.cacheMu.Unlock()

	logger.LogWithDeduplication("database timeout")

	entries := recorded.TakeAll()
	if got := len(entries); got != 3 {
		t.Fatalf("expected first message, suppression summary, and next message; got %d", got)
	}
	if entries[1].Message != "duplicate log messages suppressed" {
		t.Fatalf("expected suppression summary, got %q", entries[1].Message)
	}
	if got := entries[1].ContextMap()["count"]; got != int64(1) {
		t.Fatalf("expected suppressed count 1, got %v", got)
	}
}

func TestLogWithDeduplicationConcurrent(t *testing.T) {
	core, recorded := observer.New(zapcore.InfoLevel)
	logger := &SugaredLogger{
		SugaredLogger: zap.New(core).Sugar(),
		logCache:      make(map[string]logEntry),
		dedupInterval: time.Minute,
	}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			logger.LogWithDeduplication("rate limited")
		}()
	}
	wg.Wait()

	if got := recorded.Len(); got != 1 {
		t.Fatalf("expected one emitted message, got %d", got)
	}
}

func TestNewWithNoOutputs(t *testing.T) {
	logger := New(WithConfig(Config{Console: false, File: false}))

	logger.Info("discarded")
	if err := logger.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}
}
