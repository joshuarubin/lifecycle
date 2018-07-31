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

// Done is used to indicate, asynchronously, when something is complete. It
// is used as follows:
//
//	func Something(ctx context.Context) lifecycle.Done {
//	 	done := make(chan struct{})
//		go func() {
//			// do something
//			select {
//			// other cases
//			case <-ctx.Done():
//				// shutdown cleanly
//				close(done)
//				return
//			}
//		}()
//		return done
//	}
type Done <-chan struct{}

// DoneFunc provides a function that returns when the done channel does, for
// use with Go and Defer.
func DoneFunc(done Done) func() {
	return func() { <-done }
}

type manager struct {
	*errgroup.Group

	timeout time.Duration
	sigs    []os.Signal

	ctx      context.Context
	cancel   func()
	gctx     context.Context
	deferred []func() error
}

type contextKey struct{}

func fromContext(ctx context.Context) (m *manager, ok bool) {
	m, ok = ctx.Value(contextKey{}).(*manager)
	return
}

// New returns a lifecycle manager with context derived from that
// provided.
func New(ctx context.Context, opts ...Option) context.Context {
	m := &manager{
		deferred: []func() error{},
	}

	ctx = context.WithValue(ctx, contextKey{}, m)

	m.sigs = make([]os.Signal, len(defaultSignals))
	copy(m.sigs, defaultSignals)

	m.ctx, m.cancel = context.WithCancel(ctx)
	m.Group, m.gctx = errgroup.WithContext(context.Background())

	for _, o := range opts {
		o(m)
	}

	return m.ctx
}

// ErrNoManager is returned by Go(), Defer(), and Wait() if called and the
// passed in context was not created with New()
var ErrNoManager = fmt.Errorf("lifecycle manager not in context")

func errFunc(fn func()) func() error {
	return func() error {
		fn()
		return nil
	}
}

// Go run a function in a new goroutine and the first to return in error in this
// way cancels the group; the error retained.
func Go(ctx context.Context, f ...func()) error {
	m, ok := fromContext(ctx)
	if !ok {
		return ErrNoManager
	}

	for _, fn := range f {
		m.Group.Go(errFunc(fn))
	}

	return nil
}

// GoErr run a function in a new goroutine and the first to return in error in this
// way cancels the group; the error retained.
func GoErr(ctx context.Context, f ...func() error) error {
	m, ok := fromContext(ctx)
	if !ok {
		return ErrNoManager
	}

	for _, fn := range f {
		m.Group.Go(fn)
	}

	return nil
}

// Defer adds funcs that should be called after the Go funcs complete (either
// clean or with errors) or a signal is received.
func Defer(ctx context.Context, deferred ...func()) error {
	m, ok := fromContext(ctx)
	if !ok {
		return ErrNoManager
	}

	for _, fn := range deferred {
		m.deferred = append(m.deferred, errFunc(fn))
	}

	return nil
}

// DeferErr adds funcs that should be called after the Go funcs complete (either
// clean or with errors) or a signal is received.
func DeferErr(ctx context.Context, deferred ...func() error) error {
	m, ok := fromContext(ctx)
	if !ok {
		return ErrNoManager
	}

	m.deferred = append(m.deferred, deferred...)

	return nil
}

// Wait blocks until all go routines have been completed.
//
// First all goroutines registered with Go have to complete (either cleanly or
// with an error).
//
// Next, the funcs registered with Defer are executed.
//
// If a signal is received, e.g. SIGINT or SIGTERM, the context returned by
// New() is canceled.
//
// The goroutines registered with Go should stop and clean up when the context
// returned by New() is canceled.
//
// WithTimeout() can be used to set a maximum amount of time, from signal or
// context cancellation, that Wait will wait before returning.
//
// The error returned is the first one returned by any goroutines registered
// with Go. If none of those return an error, then the error is the first one
// returned by a goroutine registered with Defer.
func Wait(ctx context.Context) error {
	m, ok := fromContext(ctx)
	if !ok {
		return ErrNoManager
	}

	err := m.runPrimaryGroup()
	m.cancel()
	if err != nil {
		_ = m.runDeferredGroup() // #nosec
		return err
	}

	return m.runDeferredGroup()
}

// runPrimaryGroup waits for all registered routines to
// complete, returning on an error from any of them, or from
// the receipt of a registered signal, or from a context cancelation.
func (m *manager) runPrimaryGroup() error {
	select {
	case <-m.signalReceived():
	case err := <-m.runPrimaryGroupRoutines():
		return err
	case <-m.ctx.Done():
		return m.ctx.Err()
	case <-m.gctx.Done():
		// the error from the gctx errgroup will be returned
		// from errgroup.Wait() later in Handle()
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
	}
}

// A channel that receives any os signals registered to be received.
// If not configured to receive signals, it will receive nothing.
func (m *manager) signalReceived() chan os.Signal {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, m.sigs...)
	return sigCh
}

// A channel that notifies of errors caused while waiting for subroutines to finish.
func (m *manager) runPrimaryGroupRoutines() chan error {
	errs := make(chan error, 1)
	go func() { errs <- m.Wait() }()
	return errs
}

func (m *manager) runDeferredGroupRoutines() chan error {
	errs := make(chan error, 1)
	dg := errgroup.Group{}
	dg.Go(m.Wait) // Wait for the primary group as well
	for _, f := range m.deferred {
		dg.Go(f)
	}
	go func() {
		errs <- dg.Wait()
	}()
	return errs
}
