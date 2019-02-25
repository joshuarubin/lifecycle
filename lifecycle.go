package lifecycle

import (
	"context"
	"fmt"
	"os"
	"os/signal"
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
	gctx     context.Context
	deferred []func() error
	panic    chan interface{}
}

type contextKey struct{}

func fromContext(ctx context.Context) *manager {
	m, ok := ctx.Value(contextKey{}).(*manager)
	if !ok {
		panic(ErrNoManager)
	}
	return m
}

// New returns a lifecycle manager with context derived from that
// provided.
func New(ctx context.Context, opts ...Option) context.Context {
	m := &manager{
		deferred: []func() error{},
		panic:    make(chan interface{}, 1),
	}

	ctx = context.WithValue(ctx, contextKey{}, m)

	m.sigs = make([]os.Signal, len(defaultSignals))
	copy(m.sigs, defaultSignals)

	m.ctx, m.cancel = context.WithCancel(ctx)
	m.group, m.gctx = errgroup.WithContext(context.Background())

	for _, o := range opts {
		o(m)
	}

	return m.ctx
}

// ErrNoManager is returned by Go(), Defer(), and Wait() if called and the
// passed in context was not created with New()
var ErrNoManager = fmt.Errorf("lifecycle: manager not in context")

func wrapFunc(ctx context.Context, fn func() error) func() error {
	m := fromContext(ctx)

	return func() error {
		defer func() {
			if r := recover(); r != nil {
				m.panic <- r
			}
		}()
		return fn()
	}
}

// Go run a function in a new goroutine
func Go(ctx context.Context, f ...func()) {
	m := fromContext(ctx)

	for _, t := range f {
		fn := t
		m.group.Go(wrapFunc(ctx, func() error {
			fn()
			return nil
		}))
	}
}

// GoErr runs a function that returns an error in a new goroutine. If any GoErr
// or DeferErr func returns an error, only the first one will be returned by
// Wait()
func GoErr(ctx context.Context, f ...func() error) {
	m := fromContext(ctx)

	for _, fn := range f {
		m.group.Go(wrapFunc(ctx, fn))
	}
}

// Defer adds funcs that should be called after the Go funcs complete (either
// clean or with errors) or a signal is received
func Defer(ctx context.Context, deferred ...func()) {
	m := fromContext(ctx)

	for _, t := range deferred {
		fn := t
		m.deferred = append(m.deferred, wrapFunc(ctx, func() error {
			fn()
			return nil
		}))
	}
}

// DeferErr adds funcs, that return errors, that should be called after the Go
// funcs complete (either clean or with errors) or a signal is received. If any
// GoErr or DeferErr func returns an error, only the first one will be returned
// by Wait()
func DeferErr(ctx context.Context, deferred ...func() error) {
	m := fromContext(ctx)

	for _, fn := range deferred {
		m.deferred = append(m.deferred, wrapFunc(ctx, fn))
	}
}

// Wait blocks until all go routines have been completed.
//
// All funcs registered with Go and Defer _will_ complete under every
// circumstance except a panic
//
// Funcs passed to Defer begin (and the context returned by New() is canceled)
// when any of:
//
//   - All funcs registered with Go complete successfully
//   - Any func registered with Go returns an error
//   - A signal is received (by default SIGINT or SIGTERM, but can be changed by
//     WithSignals
//
// Funcs registered with Go should stop and clean up when the context
// returned by New() is canceled. If the func accepts a context argument, it
// will be passed the context returned by New().
//
// WithTimeout() can be used to set a maximum amount of time, starting with the
// context returned by New() is canceled, that Wait will wait before returning.
//
// The returned err is the first non-nil error returned by any func registered
// with Go or Defer, otherwise nil.
func Wait(ctx context.Context) error {
	m := fromContext(ctx)

	err := m.runPrimaryGroup()
	m.cancel()
	if err != nil {
		_ = m.runDeferredGroup() // #nosec
		return err
	}

	return m.runDeferredGroup()
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
	select {
	case sig := <-m.signalReceived():
		return ErrSignal{sig}
	case err := <-m.runPrimaryGroupRoutines():
		return err
	case <-m.ctx.Done():
		return m.ctx.Err()
	case <-m.gctx.Done():
		// the error from the gctx errgroup will be returned
		// from errgroup.Wait() later in runDeferredGroupRoutines
	case r := <-m.panic:
		panic(r)
	}
	return nil
}

func (m *manager) runDeferredGroup() error {
	ctx := context.Background()

	if m.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, m.timeout)
		defer cancel() // releases resources if deferred functions return early
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-m.runDeferredGroupRoutines():
		return err
	case r := <-m.panic:
		panic(r)
	}
}

// A channel that receives any os signals registered to be received.
// If not configured to receive signals, it will receive nothing.
func (m *manager) signalReceived() <-chan os.Signal {
	sigCh := make(chan os.Signal, 1)
	if len(m.sigs) > 0 {
		signal.Notify(sigCh, m.sigs...)
	}
	return sigCh
}

// A channel that notifies of errors caused while waiting for subroutines to finish.
func (m *manager) runPrimaryGroupRoutines() <-chan error {
	errs := make(chan error, 1)
	go func() { errs <- m.group.Wait() }()
	return errs
}

func (m *manager) runDeferredGroupRoutines() <-chan error {
	errs := make(chan error, 1)
	dg := errgroup.Group{}
	dg.Go(m.group.Wait) // Wait for the primary group as well
	for _, f := range m.deferred {
		dg.Go(f)
	}
	go func() {
		errs <- dg.Wait()
	}()
	return errs
}
