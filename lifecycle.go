package lifecycle // import "zvelo.io/lifecycle"

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
)

// ErrorStarter is an interface that can be implemented by anything that would
// like to integrate well with lifecycle handlers.
type ErrorStarter interface {
	Start(context.Context) (Done, error)
}

// Starter is an interface that can be implemented by anything that would like
// to integrate well with lifecycle handlers.
type Starter interface {
	Start(context.Context) Done
}

// Done is used to indicate, asynchronously, when something is complete. It
// should be used as follows:
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

// DoneFunc provides a simple mechanism to create a function from Done that can
// be used with Go and Defer.
func DoneFunc(done Done) func() error {
	return func() error {
		<-done
		return nil
	}
}

type options struct {
	timeout    time.Duration
	whenDone   bool
	sigs       []os.Signal
	handleSigs bool
}

type handler struct {
	*errgroup.Group
	ctx     context.Context
	gctx    context.Context
	options *options
	funcs   []func() error
	cancel  func()
}

// An Option is used to configure new lifecycle handlers
type Option func(*options)

// ShutdownWhenDone indicates that Manage should wait for Go funcs to complete.
func ShutdownWhenDone() Option {
	return func(o *options) {
		o.whenDone = true
	}
}

// ShutdownWhenSignaled causes Manage to wait for Go funcs to finish, if
// ShutdownWhenDone was used or until a signal is received. The signals it will
// wait for can be defined with WithSigs or will default to DefaultSignals
func ShutdownWhenSignaled(val ...os.Signal) Option {
	return func(o *options) {
		if len(val) == 0 {
			val = DefaultSignals
		}
		o.sigs = val
		o.handleSigs = true
	}
}

// WithShutdownTimeout sets an upper limit for how much time Manage will wait to
// return. After the Go funcs finish, if ShutdownWhenDone was used, or after a
// signal is received if ShutdownWhenSignaled was used, this timer starts. From
// that point, Manage will return if any Go or Defer function takes longer than
// this value.
func WithShutdownTimeout(val time.Duration) Option {
	return func(o *options) {
		o.timeout = val
	}
}

// Manager is used to manage the lifetime of an application and ensure that it
// shuts down cleanly.
type Manager interface {
	// Go calls the given function in a new goroutine
	Go(func() error)

	// Manage initiates the shutdown handler. First, if ShutdownWhenDone was
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

	// Defer adds funcs that should be called after the Go funcs finish, if
	// ShutdownWhenDone was used, or after a signal is received if
	// ShutdownWhenSignaled was used. Manage then calls the funcs added here.
	// Note that funcs added with Defer are equivalent.
	Defer(...func() error)
}

// DefaultSignals are the default signals that will be watched when
// ShutdownWhenSignaled is used.
var DefaultSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM}

func defaults() *options {
	return &options{
		sigs: DefaultSignals,
	}
}

// New returns a new lifecycle handler
func New(ctx context.Context, opts ...Option) (context.Context, Manager) {
	o := defaults()
	for _, opt := range opts {
		opt(o)
	}

	ctx, cancel := context.WithCancel(ctx)
	group, gctx := errgroup.WithContext(context.Background())

	return ctx, &handler{
		options: o,
		cancel:  cancel,
		Group:   group,
		ctx:     ctx,
		gctx:    gctx,
	}
}

func (h *handler) Defer(funcs ...func() error) {
	h.funcs = append(h.funcs, funcs...)
}

func (h *handler) waitForDoneOrSigs() error {
	if !h.options.handleSigs && !h.options.whenDone {
		return nil
	}

	var sigChan chan os.Signal

	if h.options.handleSigs {
		var sigs []string
		for _, sig := range h.options.sigs {
			sigs = append(sigs, sig.String())
		}

		sigChan = make(chan os.Signal, 1)
		signal.Notify(sigChan, h.options.sigs...)
	}

	errCh := make(chan error, 1)

	if h.options.whenDone {
		go func() { errCh <- h.Wait() }()
	}

	var err error

	select {
	case err = <-errCh:
	case sig := <-sigChan:
		log.Printf("lifecycle: received %v signal, stopping...", sig)
	case <-h.ctx.Done():
		err = h.ctx.Err()
	case <-h.gctx.Done():
		// return nil here, the error from the errgroup will be returned
		// from errgroup.Wait() later in Manage()
	}

	return err
}

func (h *handler) Manage() error {
	if err := h.waitForDoneOrSigs(); err != nil {
		return err
	}

	h.cancel()

	var wait errgroup.Group

	wait.Go(h.Wait)

	if len(h.funcs) > 0 {
		for _, fn := range h.funcs {
			wait.Go(fn)
		}
	}

	errCh := make(chan error, 1)
	go func() { errCh <- wait.Wait() }()

	ctx := context.Background()

	if h.options.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.options.timeout)
		defer cancel()
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		if err != nil {
			return err
		}

		return nil
	}
}
