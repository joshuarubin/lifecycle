package lifecycle_test

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"zvelo.io/lifecycle"
)

func TestEmptyLifecycle(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	// A lifecycle manager with no configuration should immediately return
	// on manager with no error and not block.
	err := lifecycle.Wait(ctx)
	if err != nil {
		t.Fatalf("empty lifecycle error: %v", err)
	}
}

func TestSingleRoutine(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	// A lifecycle manager with a single registered routine should immediately execute
	// the routine without needing to call Wait.
	var ran int64
	lifecycle.Go(ctx, func() error { atomic.StoreInt64(&ran, 1); return nil })
	time.Sleep(100 * time.Millisecond)
	if atomic.LoadInt64(&ran) != 1 {
		t.Error("lifecycle manager did not immediately run registered routine.")
	}
}

func TestPrimaryError(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	// A manager with a single erroring registered routine should return that
	// error on Wait
	lifecycle.Go(ctx, func() error { return errors.New("errored") })
	err := lifecycle.Wait(ctx)

	if err == nil {
		t.Fatal("error expected but not received.")
	}
	if err.Error() != "errored" {
		t.Fatalf("expected error of value \"errored\", but received: %v", err)
	}
}

func TestMultiplePrimaryErrors(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	// when multiple routines will error, the first error should be returned
	// without waiting for the second routine to finish.
	lifecycle.Go(ctx, func() error { return errors.New("error1") })
	err := lifecycle.Wait(ctx)

	if err == nil {
		t.Fatal("error expected but none received.")
	}
	if err.Error() != "error1" {
		t.Fatalf("expected error of value \"error1\", but received: %v", err)
	}
}

func TestSingleDeferred(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	// A manager with no primary routines and one deferred routine should
	// execute the deferred routine on Wait. Deferred routines do not
	// run immediately, requiring that Managebe explicitly invoked.
	ran := false
	lifecycle.Defer(ctx, func() error { ran = true; return nil })
	err := lifecycle.Wait(ctx)
	if err != nil {
		t.Fatalf("unexpected error on Wait: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if !ran {
		t.Error("lifecycle manager did not run deferred routine upon Wait.")
	}
}

func TestSingleDeferredError(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	// A manager with no primary routines and one deferred routine should
	// execute the deferred routine on Wait and return its error.
	lifecycle.Defer(ctx, func() error { return errors.New("deferred error") })
	err := lifecycle.Wait(ctx)
	if err == nil {
		t.Fatal("Manager with an erroring deferred expected error, but received none.")
	}
	if err.Error() != "deferred error" {
		t.Fatalf("expected \"deferred error\" but got: %v", err)
	}
}

func TestMultipleDeferredErrors(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	// A manager with no primary routines and multiple deferred routines should
	// execute the deferred routines, and return the first deferred error, not the last.
	lifecycle.Defer(ctx, func() error { return errors.New("deferred error1") })
	lifecycle.Defer(ctx, func() error {
		time.Sleep(500 * time.Millisecond)
		return errors.New("deferred error2")
	})
	err := lifecycle.Wait(ctx)
	if err == nil {
		t.Fatal("Manager with an erroring deferred expected error, but received none.")
	}
	if err.Error() != "deferred error1" {
		t.Fatalf("expected \"deferred error1\" but got: %v", err)
	}
}

func TestPrimaryAndSecondary(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	// A manager with both a primary and deferred routine should execute both.
	var primaryRan, deferredRan bool
	lifecycle.Go(ctx, func() error { primaryRan = true; return nil })
	lifecycle.Defer(ctx, func() error { deferredRan = true; return nil })
	err := lifecycle.Wait(ctx)
	if err != nil {
		t.Fatalf("unexpected wait error: %v", err)
	}
	if !primaryRan {
		t.Fatalf("primary routine did not run.")
	}
	if !deferredRan {
		t.Fatalf("deferred routine did not run.")
	}
}

func TestDeferredOnPrimaryError(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	// a manager with a primary error should still run deferred routines.
	var deferredRan bool
	lifecycle.Go(ctx, func() error { return errors.New("primary error") })
	lifecycle.Defer(ctx, func() error { deferredRan = true; return nil })
	err := lifecycle.Wait(ctx)
	if err == nil {
		t.Fatal("manager did not return primary routine error.")
	}
	if !deferredRan {
		t.Fatal("deferred manager did not run on primary manager error.")
	}
}

func TestDeferredTimeout(t *testing.T) {
	ctx := lifecycle.New(
		context.Background(),
		lifecycle.WithTimeout(10*time.Millisecond))

	// a manager with a deferred function that takes longer than the configured
	// lifecycle timeout should return with a timeout error.
	lifecycle.Defer(ctx, func() error { time.Sleep(30 * time.Second); return nil })
	err := lifecycle.Wait(ctx)
	if err == nil {
		t.Fatal("deferred timeout expected a timeout error at 10ms.")
	}
	if err != context.DeadlineExceeded {
		t.Fatalf("expected 'deadline exceeded' error but got: %v", err)
	}
}

func TestContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ctx = lifecycle.New(ctx,
		lifecycle.WithTimeout(6*time.Second))

	// a manger whose context is canceled should return before subroutines
	// complete with a context cancelation error.
	lifecycle.Go(ctx, func() error { time.Sleep(10 * time.Second); return nil })
	lifecycle.Defer(ctx, func() error { time.Sleep(10 * time.Second); return nil })
	go func() {
		time.Sleep(1 * time.Second)
		cancel()
	}()
	err := lifecycle.Wait(ctx)
	if err == nil {
		t.Fatal("canceled context expected canceled error.")
	}
	if err != context.Canceled {
		t.Fatalf("expected 'context canceled' error but got: %v", err)
	}
}

func TestSignalCancels(t *testing.T) {
	ctx := lifecycle.New(context.Background(),
		lifecycle.WithTimeout(1*time.Second),
		lifecycle.WithSignals(syscall.SIGUSR1)) // SIGUSR1 plays nicely with tests

	// A long-running goroutine, when signaled, should invoke the deferred
	// functions and wait up to timeout before interrupting the laggard.

	deferredRan := int64(0)
	lifecycle.Go(ctx, func() error { time.Sleep(1 * time.Minute); return nil })
	lifecycle.Defer(ctx, func() error { atomic.StoreInt64(&deferredRan, 1); return nil })
	go func() {
		time.Sleep(100 * time.Millisecond)
		process, _ := os.FindProcess(syscall.Getpid())
		_ = process.Signal(syscall.SIGUSR1)
	}()
	err := lifecycle.Wait(ctx)
	if err != context.DeadlineExceeded {
		t.Fatalf("unexpected error on signal interrupt: %v", err)
	}
	if atomic.LoadInt64(&deferredRan) != 1 {
		t.Fatal("signaled process did not run deferred func")
	}
}

func TestIgnoreSignals(t *testing.T) {
	ctx := lifecycle.New(
		context.Background(),
		lifecycle.WithTimeout(1*time.Second),
		lifecycle.WithSignals())
	lifecycle.Defer(ctx, func() error { time.Sleep(1 * time.Minute); return nil })
	go func() {
		time.Sleep(100 * time.Millisecond)
		process, _ := os.FindProcess(syscall.Getpid())
		_ = process.Signal(syscall.SIGUSR1)
	}()
	err := lifecycle.Wait(ctx)
	if err != context.DeadlineExceeded {
		t.Fatalf("expected deadline exceeded, got: %v", err)
	}
}

func TestDoneFunc(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	// Given a function that returns a channel signaling when it is done:
	var asyncCompleted bool
	doAsyncThings := func() <-chan struct{} {
		done := make(chan struct{})
		go func() {
			// ... do some things asynchronously
			time.Sleep(100 * time.Millisecond)
			// ... signaling done by closing the channel
			asyncCompleted = true
			close(done)
		}()
		return done
	}
	done := doAsyncThings()

	// Use DoneFunc to conver the signal to
	lifecycle.Go(ctx, lifecycle.DoneFunc(done))
	err := lifecycle.Wait(ctx)
	if err != nil {
		t.Fatalf("Unexpected error from wait: %v", err)
	}
	if !asyncCompleted {
		t.Fatalf("async job using DoneFunc did not complete.")
	}
}

func TestErrors(t *testing.T) {
	ctx := context.Background()
	if err := lifecycle.Go(ctx); err == nil {
		t.Error("expected an error")
	}
	if err := lifecycle.Defer(ctx); err == nil {
		t.Error("expected an error")
	}
	if err := lifecycle.Wait(ctx); err == nil {
		t.Error("expected an error")
	}
}
