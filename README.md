# lifecycle

[![GoDoc](https://godoc.org/github.com/joshuarubin/lifecycle?status.svg)](https://godoc.org/github.com/joshuarubin/lifecycle) [![Go Report Card](https://goreportcard.com/badge/github.com/joshuarubin/lifecycle)](https://goreportcard.com/report/github.com/joshuarubin/lifecycle) [![codecov](https://codecov.io/gh/joshuarubin/lifecycle/branch/master/graph/badge.svg)](https://codecov.io/gh/joshuarubin/lifecycle) [![CircleCI](https://circleci.com/gh/joshuarubin/lifecycle.svg?style=svg)](https://circleci.com/gh/joshuarubin/lifecycle)

## Overview

`lifecycle` helps manage goroutines at the application level. `context.Context` has been great for propagating cancellation signals, but not for getting any feedback about _when_ goroutines actually finish.

This package works with `context.Context` to ensure that applications don't quit before their goroutines do.

The semantics work similarly to the `go` (`lifecycle.Go`) and `defer` (`lifecycle.Defer`) keywords as well as `sync.WaitGroup.Wait` (`lifecycle.Wait`). Additionally, there are `lifecycle.GoErr` and `lifecycle.DeferErr` which only differ in that they take funcs that return errors.

`lifecycle.Wait` will block until one of the following happens:

- all funcs registered with `Go` complete successfully then all funcs registered with `Defer` complete successfully
- a func registered with `Go` returns an error, immediately canceling `ctx` and triggering `Defer` funcs to run. Once all `Go` and `Defer` funcs complete, `Wait` will return the error
- a signal (by default `SIGINT` and `SIGTERM`, but configurable with `WithSignals`) is received, immediately canceling `ctx` and triggering `Defer` funcs to run. Once all `Go` and `Defer` funcs complete, `Wait` will return `ErrSignal`
- a func registered with `Go` or `Defer` panics. the panic will be propagated to the goroutine that `Wait` runs in. there is no attempt, in case of a panic, to manage the state within the `lifecycle` package.

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
