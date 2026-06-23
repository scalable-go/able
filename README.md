# able

## Logging

The logging package lives under `logs/` and is imported as:

```go
import able "github.com/scalable-go/able/logs"
```

### Default logger

Use the package-level `Log` for simple applications:

```go
func main() {
	able.Log.Infow("service started", "addr", ":8080")
	defer able.Log.Sync()
}
```

### Request context fields

Initialize the request context at the boundary, then use `WithContext` inside the call chain:

```go
func handler(w http.ResponseWriter, r *http.Request) {
	ctx := able.InitContext(r.Context())
	logger := able.Log.WithContext(ctx)

	logger.Infow("request received",
		"method", r.Method,
		"path", r.URL.Path,
	)
}
```

`WithContext` reads these context keys when present:

- `request`
- `response`
- `context`
- `category`
- `ip`
- `type`
- `sub_type`
- `trace_id`

If `trace_id` is missing, the logger generates a new one for the emitted log entry.

### Custom logger

Create an isolated logger when a service, worker, or test needs its own output policy:

```go
logger := able.New(
	able.WithLevel(zapcore.DebugLevel),
	able.WithConsole(true),
	able.WithFile("logs/worker.log"),
	able.WithDedupInterval(30*time.Second),
)
defer logger.Sync()

logger.Debugw("worker configured", "queue", "emails")
```

### Environment variables

`NewFromEnv` and the package-level `Log` read these variables:

| Variable | Example | Description |
| --- | --- | --- |
| `LOG_LEVEL` | `debug`, `info`, `warn`, `error` | Explicit log level. |
| `APP_ENV` | `develop`, `production` | Fallback level selector when `LOG_LEVEL` is empty. |
| `LOG_CONSOLE` | `true`, `false` | Enables stdout logging. |
| `LOG_FILE` | `true`, `false` | Enables rotating file logging. |
| `LOG_FILE_PATH` | `logs/app.log` | File output path. |
| `LOG_DEDUP_INTERVAL` | `10s`, `1m` | Window for duplicate-message suppression. |

### Duplicate suppression

Use `LogWithDeduplication` for noisy hot paths where the message text is stable:

```go
for {
	if err := poll(); err != nil {
		able.Log.LogWithDeduplication("poll failed")
		continue
	}
}
```

For dynamic failures, keep high-cardinality values as structured fields on normal logs instead of embedding them in the deduplication message.
