// Package lifecycle manages the startup and shutdown of concurrent goroutines.
//
// All public functions that accept a context.Context (except New and Exists)
// panic if the context does not carry a lifecycle manager created by New.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
)

var defaultSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM}

type manager struct {
	group *errgroup.Group

	timeout time.Duration
	sigs    []os.Signal

	ctx      context.Context
	cancel   func()
	groupCtx context.Context
	mu       sync.Mutex
	deferred []func() error
	panic      chan any
	waitCalled atomic.Bool
	waited     atomic.Bool
}

type contextKey struct{}

func fromContext(ctx context.Context) *manager {
	m, ok := ctx.Value(contextKey{}).(*manager)
	if !ok {
		panic(fmt.Errorf("lifecycle: manager not in context"))
	}
	return m
}

// New returns a lifecycle manager with context derived from that
// provided.
func New(ctx context.Context, opts ...Option) context.Context {
	m := &manager{
		deferred: []func() error{},
		panic:    make(chan any, 1),
	}

	ctx = context.WithValue(ctx, contextKey{}, m)

	m.sigs = make([]os.Signal, len(defaultSignals))
	copy(m.sigs, defaultSignals)

	m.ctx, m.cancel = context.WithCancel(ctx)
	// The errgroup uses context.Background() intentionally: gctx is only
	// canceled when a Go func returns an error, while m.ctx.Done() handles
	// other cancellation sources in runPrimaryGroup's select.
	m.group, m.groupCtx = errgroup.WithContext(context.Background())

	for _, o := range opts {
		o(m)
	}

	return m.ctx
}

// Exists returns true if the context has a lifecycle manager attached
func Exists(ctx context.Context) bool {
	_, ok := ctx.Value(contextKey{}).(*manager)
	return ok
}

func (m *manager) wrapCtxFunc(ctx context.Context, fn func(ctx context.Context) error) func() error {
	return func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				debug.PrintStack()
				// Non-blocking send: only the first panic is
				// propagated to Wait. Subsequent panics are returned
				// as errors so the errgroup records a failure.
				select {
				case m.panic <- r:
				default:
					err = fmt.Errorf("lifecycle: panic: %v", r)
				}
			}
		}()
		return fn(ctx)
	}
}

func (m *manager) wrapFunc(fn func() error) func() error {
	return func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				debug.PrintStack()
				select {
				case m.panic <- r:
				default:
					err = fmt.Errorf("lifecycle: panic: %v", r)
				}
			}
		}()
		return fn()
	}
}

// Go runs each function in a new goroutine.
// Must not be called after Wait returns.
func Go(ctx context.Context, f ...func()) {
	m := fromContext(ctx)

	if m.waited.Load() {
		panic(fmt.Errorf("lifecycle: Go called after Wait"))
	}

	for _, fn := range f {
		if fn == nil {
			continue
		}
		m.group.Go(m.wrapFunc(func() error {
			fn()
			return nil
		}))
	}
}

// GoCtx runs each function that takes a context in a new goroutine.
// Must not be called after Wait returns.
func GoCtx(ctx context.Context, f ...func(ctx context.Context)) {
	m := fromContext(ctx)

	if m.waited.Load() {
		panic(fmt.Errorf("lifecycle: GoCtx called after Wait"))
	}

	for _, fn := range f {
		if fn == nil {
			continue
		}
		m.group.Go(m.wrapCtxFunc(ctx, func(ctx context.Context) error {
			fn(ctx)
			return nil
		}))
	}
}

// GoErr runs each function that returns an error in a new goroutine. If any
// GoErr, GoCtxErr, DeferErr or DeferCtxErr func returns an error, only the
// first one will be returned by Wait.
// Must not be called after Wait returns.
func GoErr(ctx context.Context, f ...func() error) {
	m := fromContext(ctx)

	if m.waited.Load() {
		panic(fmt.Errorf("lifecycle: GoErr called after Wait"))
	}

	for _, fn := range f {
		if fn == nil {
			continue
		}
		m.group.Go(m.wrapFunc(fn))
	}
}

// GoCtxErr runs each function that takes a context and returns an error in a
// new goroutine. If any GoErr, GoCtxErr, DeferErr or DeferCtxErr func returns
// an error, only the first one will be returned by Wait.
// Must not be called after Wait returns.
func GoCtxErr(ctx context.Context, f ...func(ctx context.Context) error) {
	m := fromContext(ctx)

	if m.waited.Load() {
		panic(fmt.Errorf("lifecycle: GoCtxErr called after Wait"))
	}

	for _, fn := range f {
		if fn == nil {
			continue
		}
		m.group.Go(m.wrapCtxFunc(ctx, fn))
	}
}

// Defer adds funcs that should be called after the Go funcs complete (either
// clean or with errors) or a signal is received. Deferred funcs run
// sequentially in LIFO order. They may execute concurrently with Go funcs that
// are still shutting down.
// Must not be called after Wait returns.
func Defer(ctx context.Context, deferred ...func()) {
	m := fromContext(ctx)

	if m.waited.Load() {
		panic(fmt.Errorf("lifecycle: Defer called after Wait"))
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, fn := range deferred {
		if fn == nil {
			continue
		}
		m.deferred = append(m.deferred, m.wrapFunc(func() error {
			fn()
			return nil
		}))
	}
}

// DeferCtx adds funcs, that take a context, that should be called after the Go
// funcs complete (either clean or with errors) or a signal is received.
// Must not be called after Wait returns.
//
// The context passed to each function is the lifecycle context. Although it will
// be canceled by the time deferred functions execute, the context values
// (e.g., trace IDs, loggers) remain accessible. This is intentional: deferred
// functions often need these values for structured logging, tracing, or metric
// reporting during cleanup.
//
// Callers that need a live (non-canceled) context for cleanup operations
// (e.g., srv.Shutdown) should use DeferErr with context.Background() instead.
func DeferCtx(ctx context.Context, deferred ...func(context.Context)) {
	m := fromContext(ctx)

	if m.waited.Load() {
		panic(fmt.Errorf("lifecycle: DeferCtx called after Wait"))
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, fn := range deferred {
		if fn == nil {
			continue
		}
		m.deferred = append(m.deferred, m.wrapCtxFunc(ctx, func(ctx context.Context) error {
			fn(ctx)
			return nil
		}))
	}
}

// DeferErr adds funcs, that return errors, that should be called after the Go
// funcs complete (either clean or with errors) or a signal is received. If any
// GoErr, GoCtxErr, DeferErr or DeferCtxErr func returns an error, only the
// first one will be returned by Wait.
// Must not be called after Wait returns.
func DeferErr(ctx context.Context, deferred ...func() error) {
	m := fromContext(ctx)

	if m.waited.Load() {
		panic(fmt.Errorf("lifecycle: DeferErr called after Wait"))
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, fn := range deferred {
		if fn == nil {
			continue
		}
		m.deferred = append(m.deferred, m.wrapFunc(fn))
	}
}

// DeferCtxErr adds funcs, that take a context and return an error, that should
// be called after the Go funcs complete (either clean or with errors) or a
// signal is received. If any GoErr, GoCtxErr, DeferErr or DeferCtxErr func
// returns an error, only the first one will be returned by Wait.
// Must not be called after Wait returns.
//
// The context passed to each function is the lifecycle context. Although it will
// be canceled by the time deferred functions execute, the context values
// (e.g., trace IDs, loggers) remain accessible. This is intentional: deferred
// functions often need these values for structured logging, tracing, or metric
// reporting during cleanup.
//
// Callers that need a live (non-canceled) context for cleanup operations
// (e.g., srv.Shutdown) should use DeferErr with context.Background() instead.
func DeferCtxErr(ctx context.Context, deferred ...func(context.Context) error) {
	m := fromContext(ctx)

	if m.waited.Load() {
		panic(fmt.Errorf("lifecycle: DeferCtxErr called after Wait"))
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, fn := range deferred {
		if fn == nil {
			continue
		}
		m.deferred = append(m.deferred, m.wrapCtxFunc(ctx, fn))
	}
}

// Wait blocks until all goroutines have completed.
//
// All funcs registered with Go and Defer _will_ complete under every
// circumstance except a panic or a timeout (see WithTimeout).
//
// Funcs passed to Defer begin (and the context returned by New is canceled)
// when any of:
//
//   - All funcs registered with Go complete successfully
//   - Any func registered with Go returns an error
//   - A signal is received (by default SIGINT or SIGTERM, configurable with
//     WithSignals)
//   - The parent context passed to New is canceled
//
// Note: signals received between New and Wait are not caught. Call Wait
// promptly after registering your Go and Defer funcs.
//
// Deferred funcs run sequentially in LIFO order but concurrently with any Go
// funcs that are still shutting down. Both groups must finish before Wait
// returns.
//
// Funcs registered with Go should stop and clean up when the context
// returned by New is canceled. If the func accepts a context argument, it
// will be passed the context returned by New.
//
// WithTimeout can be used to set a maximum amount of time, starting when the
// context returned by New is canceled, that Wait will wait before returning.
// When the timeout fires, any deferred func currently executing will finish,
// but remaining deferred funcs in the queue will be skipped.
//
// Error priority (highest to lowest): panic > signal > context cancellation >
// first Go/GoErr/GoCtxErr error > first Defer/DeferErr/DeferCtxErr error.
//
// When both a Go func and a Defer func return errors, Wait returns a joined
// error (via [errors.Join]) so that both are accessible with [errors.Is] and
// [errors.As].
func Wait(ctx context.Context) error {
	m := fromContext(ctx)

	if !m.waitCalled.CompareAndSwap(false, true) {
		panic(fmt.Errorf("lifecycle: Wait called more than once"))
	}

	primaryErr := m.runPrimaryGroup()
	m.waited.Store(true)
	m.cancel()
	groupErr, deferErr := m.runDeferredGroup()
	// When runPrimaryGroup returned nil (the groupCtx.Done shortcut),
	// the group error from runDeferredGroupRoutines is the real primary
	// error. When runPrimaryGroup returned non-nil, the group error is
	// either a duplicate or a consequence of the shutdown (e.g.,
	// http.ErrServerClosed) and should not be surfaced separately.
	if primaryErr == nil {
		primaryErr = groupErr
	}
	return joinErrors(primaryErr, deferErr)
}

// joinErrors combines multiple errors. Unlike [errors.Join], it returns the
// error as-is when only one non-nil error exists, preserving direct equality
// comparisons (e.g., err == context.Canceled).
func joinErrors(errs ...error) error {
	var nonNil []error
	for _, e := range errs {
		if e != nil {
			nonNil = append(nonNil, e)
		}
	}
	switch len(nonNil) {
	case 0:
		return nil
	case 1:
		return nonNil[0]
	default:
		return errors.Join(nonNil...)
	}
}

// ErrSignal is returned by Wait if the reason it returned was because a signal
// was caught
type ErrSignal struct {
	os.Signal
}

func (e ErrSignal) Error() string {
	return fmt.Sprintf("lifecycle: caught signal: %v", e.Signal)
}

// runPrimaryGroup waits for all registered routines to
// complete, returning on an error from any of them, or from
// the receipt of a registered signal, or from a context cancelation.
func (m *manager) runPrimaryGroup() error {
	sigCh := make(chan os.Signal, 1)
	if len(m.sigs) > 0 {
		signal.Notify(sigCh, m.sigs...)
	}
	defer signal.Stop(sigCh)

	select {
	case sig := <-sigCh:
		return ErrSignal{sig}
	case err := <-m.runPrimaryGroupRoutines():
		return err
	case <-m.ctx.Done():
		return m.ctx.Err()
	case <-m.groupCtx.Done():
		// A Go func returned an error, canceling gctx. Return nil here
		// so that Wait proceeds to run deferred functions immediately
		// (rather than waiting for all Go funcs to finish first). The
		// original error will be recovered via m.group.Wait() in
		// runDeferredGroupRoutines.
	case r := <-m.panic:
		panic(r)
	}
	return nil
}

func (m *manager) runDeferredGroup() (groupErr, deferErr error) {
	timeoutCtx := context.Background()

	if m.timeout > 0 {
		var cancel context.CancelFunc
		timeoutCtx, cancel = context.WithTimeout(timeoutCtx, m.timeout)
		defer cancel() // releases resources if deferred functions return early
	}

	select {
	case <-timeoutCtx.Done():
		return nil, timeoutCtx.Err()
	case result := <-m.runDeferredGroupRoutines(timeoutCtx):
		return result.groupErr, result.deferErr
	case r := <-m.panic:
		panic(r)
	}
}

// A channel that notifies of errors caused while waiting for subroutines to finish.
func (m *manager) runPrimaryGroupRoutines() <-chan error {
	errs := make(chan error, 1)
	go func() { errs <- m.group.Wait() }()
	return errs
}

type deferResult struct {
	groupErr error
	deferErr error
}

func (m *manager) runDeferredGroupRoutines(timeoutCtx context.Context) <-chan deferResult {
	m.mu.Lock()
	deferred := m.deferred
	m.mu.Unlock()

	done := make(chan struct{})
	var deferErr error
	go func() {
		defer close(done)
		for i := len(deferred) - 1; i >= 0; i-- {
			if timeoutCtx.Err() != nil {
				break
			}
			if derr := deferred[i](); deferErr == nil {
				deferErr = derr
			}
		}
	}()

	results := make(chan deferResult, 1)
	go func() {
		// group.Wait is safe to call multiple times (returns the same
		// error). This second call ensures all primary Go funcs finish
		// before the deferred phase completes, even when runPrimaryGroup
		// returned early via groupCtx.Done() or a signal.
		gerr := m.group.Wait()
		<-done
		results <- deferResult{groupErr: gerr, deferErr: deferErr}
	}()

	return results
}
