package lifecycle

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
)

var DefaultSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM}

// Manager is used to manage the lifetime of an application and ensure that it
// shuts down cleanly.
type Manager interface {
	// Go calls the given function in a new goroutine
	Go(...func() error)

	// Defer adds funcs that should be called after the Go funcs finish, if
	// ShutdownWhenDone was used, or after a signal is received if
	// ShutdownWhenSignaled was used. Manage then calls the funcs added here.
	// Note that funcs added with Defer are equivalent.
	Defer(...func() error)

	// Manage initiates the lifecycle manager. First, if ShutdownWhenDone was
	// used, it will wait for any function run with Go to finish. If
	// ShutdownWhenSignaled was used, any signal received will cause the handler
	// to immediately proceed to step two. Third, if WithShutdownTimeout was
	// used, the timer starts at this point. Fourth, it will execute,
	// asynchronously, all funcs added with Defer. At this point Manage will
	// return when all funcs run with Go and Defer have completed or when the
	// timer expires. The return value will be the first non-nil error returned
	// by a function it was waiting for, or context.Canceled if the timer
	// expires.
	Manage() error
}

// Done is used to indicate, asynchronously, when something is complete. It
// is used as follows:
//
//	func Something(ctx context.Context) shutdown.Done {
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
func DoneFunc(done Done) func() error {
	return func() error {
		<-done
		return nil
	}
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

// New returns a shutdown manager with context derived from that
// provided.
func New(ctx context.Context, opts ...Option) (context.Context, Manager) {
	h := &manager{
		deferred: []func() error{},
	}
	h.sigs = make([]os.Signal, len(DefaultSignals))
	copy(h.sigs, DefaultSignals)
	h.ctx, h.cancel = context.WithCancel(ctx)
	h.Group, h.gctx = errgroup.WithContext(context.Background())
	for _, o := range opts {
		o(h)
	}
	return h.ctx, h
}

// Go run a function in a new goroutine and the first to return in error in this
// way cancels the group; the error retained.
func (h *manager) Go(f ...func() error) {
	for _, fn := range f {
		h.Group.Go(fn)
	}
}

// Defer a functions to be run in a separate errgroup after the
// first, primary errgroup has completed (either clean or with errors).
func (h *manager) Defer(deferred ...func() error) {
	h.deferred = append(h.deferred, deferred...)
}

// Manage blocks until all go routines have been completed or a
// signal has been received.
//
// Manage blocks until all the routines registered using Go have completed
// (either cleanly or due to receiving an error os a signal), and then
// waits for all the deferred functions to complete (either cleanly or due
// to receiving a signal), with the timeout applied as the maximum time to wait
// for the deferred functions to complete.
//
// Manage runs all goroutines registered with .Go, canceling if any
// throw an error.  It also cancels immediately if any of the
// configured system signals are received.  It then runs all deferred
// functions, returning immediately if any fail.
func (h *manager) Manage() (err error) {
	err = h.runPrimaryGroup()
	h.cancel()
	if err != nil {
		_ = h.runDeferredGroup()
		return
	}
	return h.runDeferredGroup()
}

// runPrimaryGroup waits for all registered routines to
// complete, returning on an error from any of them, or from
// the receipt of a registered signal, or from a context cancelation.
func (h *manager) runPrimaryGroup() (err error) {
	select {
	case <-h.signalReceived():
	case err = <-h.runPrimaryGroupRoutines():
	case <-h.ctx.Done():
		err = h.ctx.Err()
	case <-h.gctx.Done():
		// the error from the gctx errgroup will be returned
		// from errgroup.Wait() later in Handle()
	}
	return
}

func (h *manager) runDeferredGroup() (err error) {
	var (
		ctx    = context.Background()
		cancel context.CancelFunc
	)
	if h.timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, h.timeout)
		defer cancel() // releases resources if deferred functions return early
	}
	select {
	case <-ctx.Done():
		err = ctx.Err()
	case err = <-h.runDeferredGroupRoutines():
	}
	return
}

// A channel that receives any os signals registered to be received.
// If not configured to receive signals, it will receive nothing.
func (h *manager) signalReceived() chan os.Signal {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, h.sigs...)
	return sigCh
}

// A channel that notifies of errors caused while waiting for subroutines to finish.
func (h *manager) runPrimaryGroupRoutines() (errs chan error) {
	errs = make(chan error, 1)
	go func() { errs <- h.Wait() }()
	return
}

func (h *manager) runDeferredGroupRoutines() (errs chan error) {
	errs = make(chan error, 1)
	dg := errgroup.Group{}
	dg.Go(h.Wait) // Wait for the primary group as well
	for _, f := range h.deferred {
		dg.Go(f)
	}
	go func() {
		errs <- dg.Wait()
	}()
	return
}
