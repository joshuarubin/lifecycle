# lifecycle

[![GoDoc](https://godoc.org/github.com/joshuarubin/lifecycle?status.svg)](https://godoc.org/github.com/joshuarubin/lifecycle) [![Go Report Card](https://goreportcard.com/badge/github.com/joshuarubin/lifecycle)](https://goreportcard.com/report/github.com/joshuarubin/lifecycle) [![codecov](https://codecov.io/gh/joshuarubin/lifecycle/branch/master/graph/badge.svg)](https://codecov.io/gh/joshuarubin/lifecycle) [![CircleCI](https://circleci.com/gh/joshuarubin/lifecycle.svg?style=svg)](https://circleci.com/gh/joshuarubin/lifecycle)

## Overview

`lifecycle` helps manage goroutines at the application level. `context.Context` has been great for propagating cancellation signals, but not for getting any feedback about _when_ goroutines actually finish.

This package works with `context.Context` to ensure that applications don't quit before their goroutines do.

## API

### Creating a lifecycle

```go
ctx := lifecycle.New(ctx, opts...)
```

Returns a new context carrying a lifecycle manager. Options:

- `lifecycle.WithTimeout(d)` — maximum time for the shutdown phase (deferred funcs + Go funcs responding to cancellation)
- `lifecycle.WithSignals(sigs...)` — signals that trigger shutdown (default: `SIGINT`, `SIGTERM`). Pass no arguments to disable signal handling.

### Checking for a lifecycle

```go
if lifecycle.Exists(ctx) { ... }
```

Returns true if the context carries a lifecycle manager.

### Registering goroutines

| Function | Signature | Notes |
|---|---|---|
| `Go` | `func()` | Fire-and-forget |
| `GoCtx` | `func(ctx context.Context)` | Receives the lifecycle context |
| `GoErr` | `func() error` | Error cancels the group |
| `GoCtxErr` | `func(ctx context.Context) error` | Both context and error |

All accept variadic arguments. Nil funcs are silently skipped. Must not be called after `Wait` returns.

### Registering deferred funcs

| Function | Signature | Notes |
|---|---|---|
| `Defer` | `func()` | Fire-and-forget cleanup |
| `DeferCtx` | `func(ctx context.Context)` | Receives the lifecycle context |
| `DeferErr` | `func() error` | Error is captured |
| `DeferCtxErr` | `func(ctx context.Context) error` | Both context and error |

Deferred funcs run sequentially in **LIFO order** (last registered, first executed), similar to Go's `defer` keyword. They may run concurrently with Go funcs that are still shutting down.

**Context note:** `DeferCtx` and `DeferCtxErr` pass the lifecycle context, which will already be canceled when deferred funcs execute. However, context _values_ (trace IDs, loggers, etc.) remain accessible. For cleanup operations that need a live context (e.g., `srv.Shutdown`), use `DeferErr` with `context.Background()` instead.

### Waiting

```go
err := lifecycle.Wait(ctx)
```

Blocks until all Go and Defer funcs complete. The shutdown phase begins when any of:

- All Go funcs complete successfully
- Any Go func returns an error
- A signal is received
- The parent context is canceled

**Error priority** (highest to lowest): panic > signal > context cancellation > first Go error > first Defer error.

**Timeout behavior:** When `WithTimeout` is set and the timeout fires, the currently executing deferred func will finish, but remaining deferred funcs in the queue are skipped.

## Example

Here is an example that shows how `lifecycle` could work with an `http.Server`:

```go
// At the top of your application
ctx := lifecycle.New(
    context.Background(),
    lifecycle.WithTimeout(30*time.Second), // optional
)

helloHandler := func(w http.ResponseWriter, req *http.Request) {
    _, _ = io.WriteString(w, "Hello, world!\n")
}

mux := http.NewServeMux()
mux.HandleFunc("/hello", helloHandler)

srv := &http.Server{
    Addr:    ":8080",
    Handler: mux,
}

lifecycle.GoErr(ctx, srv.ListenAndServe)

lifecycle.DeferErr(ctx, func() error {
    // use a background context because we already have a timeout and when
    // Defer funcs run, ctx is necessarily canceled.
    return srv.Shutdown(context.Background())
})

// Any panics in Go or Defer funcs will be passed to the goroutine that Wait
// runs in, so it is possible to handle them like this
defer func() {
    if r := recover(); r != nil {
        panic(r) // example, you probably want to do something else
    }
}()

// Then at the end of main(), or run() or however your application operates
//
// The returned err is the first non-nil error returned by any func
// registered with Go or Defer, otherwise nil.
if err := lifecycle.Wait(ctx); err != nil {
    log.Fatal(err)
}
```
