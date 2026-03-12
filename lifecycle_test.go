package lifecycle_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/joshuarubin/lifecycle"
)

func testPanic(t *testing.T, fn func()) {
	t.Helper()

	var recovered bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				recovered = true
			}
			wg.Done()
		}()
		fn()
	}()
	wg.Wait()
	if !recovered {
		t.Errorf("did not panic")
	}
}

func TestBadContext(t *testing.T) {
	testPanic(t, func() {
		lifecycle.Go(context.Background(), func() {})
	})
}

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

	var ran int64
	lifecycle.Go(ctx, func() { atomic.StoreInt64(&ran, 1) })
	err := lifecycle.Wait(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt64(&ran) != 1 {
		t.Error("lifecycle manager did not run registered routine.")
	}
}

func TestGoCtx(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	var ran int64
	lifecycle.GoCtx(ctx, func(ctx context.Context) {
		atomic.StoreInt64(&ran, 1)
	})
	err := lifecycle.Wait(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt64(&ran) != 1 {
		t.Error("GoCtx did not run registered routine")
	}
}

func TestGoCtxReceivesContext(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	var gotCtx int64
	lifecycle.GoCtx(ctx, func(ctx context.Context) {
		if lifecycle.Exists(ctx) {
			atomic.StoreInt64(&gotCtx, 1)
		}
	})
	err := lifecycle.Wait(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt64(&gotCtx) != 1 {
		t.Error("GoCtx did not pass lifecycle context to function")
	}
}

func TestGoCtxNilFunc(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	lifecycle.GoCtx(ctx, nil)
	err := lifecycle.Wait(ctx)
	if err != nil {
		t.Fatalf("unexpected error with nil func: %v", err)
	}
}

func TestGoCtxRecover(t *testing.T) {
	ctx := lifecycle.New(context.Background())
	panicErr := fmt.Errorf("test panic")

	lifecycle.GoCtx(ctx, func(ctx context.Context) { panic(panicErr) })

	defer func() {
		if r := recover(); r == nil {
			t.Error("did not panic")
		} else if r != panicErr {
			t.Error("unexpected panic")
		}
	}()

	_ = lifecycle.Wait(ctx)
}

func TestPrimaryError(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	// A manager with a single erroring registered routine should return that
	// error on Wait
	lifecycle.GoErr(ctx, func() error { return errors.New("errored") })
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

	// when multiple routines error, the first error returned should be
	// propagated by Wait.
	lifecycle.GoErr(ctx,
		func() error { return errors.New("error1") },
		func() error { return errors.New("error2") },
	)
	err := lifecycle.Wait(ctx)

	if err == nil {
		t.Fatal("error expected but none received.")
	}
}

func TestSingleDeferred(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	// A manager with no primary routines and one deferred routine should
	// execute the deferred routine on Wait. Deferred routines do not
	// run immediately, requiring that Wait be explicitly invoked.
	var ran int64
	lifecycle.Defer(ctx, func() { atomic.StoreInt64(&ran, 1) })
	err := lifecycle.Wait(ctx)
	if err != nil {
		t.Fatalf("unexpected error on Wait: %v", err)
	}
	if atomic.LoadInt64(&ran) != 1 {
		t.Error("lifecycle manager did not run deferred routine upon Wait.")
	}
}

func TestSingleDeferredError(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	// A manager with no primary routines and one deferred routine should
	// execute the deferred routine on Wait and return its error.
	lifecycle.DeferErr(ctx, func() error { return errors.New("deferred error") })
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

	// A manager with no primary routines and multiple deferred routines
	// should execute the deferred routines in reverse order, and return
	// the first executed deferred error, which might be the last deferred
	// func
	lifecycle.DeferErr(ctx, func() error { return errors.New("deferred error1") })
	lifecycle.DeferErr(ctx, func() error {
		time.Sleep(10 * time.Millisecond)
		return errors.New("deferred error2")
	})
	err := lifecycle.Wait(ctx)
	if err == nil {
		t.Fatal("Manager with an erroring deferred expected error, but received none.")
	}
	if err.Error() != "deferred error2" {
		t.Fatalf("expected \"deferred error2\" but got: %v", err)
	}
}

func TestPrimaryAndDeferred(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	// A manager with both a primary and deferred routine should execute both.
	var primaryRan, deferredRan int64
	lifecycle.Go(ctx, func() { atomic.StoreInt64(&primaryRan, 1) })
	lifecycle.Defer(ctx, func() { atomic.StoreInt64(&deferredRan, 1) })
	err := lifecycle.Wait(ctx)
	if err != nil {
		t.Fatalf("unexpected wait error: %v", err)
	}
	if atomic.LoadInt64(&primaryRan) != 1 {
		t.Fatalf("primary routine did not run.")
	}
	if atomic.LoadInt64(&deferredRan) != 1 {
		t.Fatalf("deferred routine did not run.")
	}
}

func TestDeferredOnPrimaryError(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	// a manager with a primary error should still run deferred routines.
	var deferredRan int64
	lifecycle.GoErr(ctx, func() error { return errors.New("primary error") })
	lifecycle.Defer(ctx, func() { atomic.StoreInt64(&deferredRan, 1) })
	err := lifecycle.Wait(ctx)
	if err == nil {
		t.Fatal("manager did not return primary routine error.")
	}
	if atomic.LoadInt64(&deferredRan) != 1 {
		t.Fatal("deferred manager did not run on primary manager error.")
	}
}

func TestDeferredTimeout(t *testing.T) {
	ctx := lifecycle.New(
		context.Background(),
		lifecycle.WithTimeout(10*time.Millisecond),
	)

	// a manager with a deferred function that takes longer than the configured
	// lifecycle timeout should return with a timeout error.
	lifecycle.Defer(ctx, func() { time.Sleep(30 * time.Second) })
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
	ctx = lifecycle.New(ctx)
	start := time.Now()

	// a manger whose context is canceled should return before subroutines
	// complete with a context cancelation error.
	lifecycle.Go(ctx, func() {
		select {
		case <-time.After(10 * time.Second):
		case <-ctx.Done():
		}
	})
	cancel()
	err := lifecycle.Wait(ctx)
	if err == nil {
		t.Fatal("canceled context, got nil error, expected canceled error.")
	}
	if err != context.Canceled {
		t.Fatalf("expected 'context canceled' error but got: %v", err)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Fatalf("Wait did not return as soon as the context was canceled")
	}
}

func TestSignalCancels(t *testing.T) {
	ctx := lifecycle.New(context.Background(),
		lifecycle.WithTimeout(1*time.Second),
		lifecycle.WithSignals(syscall.SIGUSR1), // SIGUSR1 plays nicely with tests
	)

	// A long-running goroutine, when signaled, should invoke the deferred
	// functions and wait up to timeout before interrupting the laggard.
	start := time.Now()

	var deferredRan int64
	lifecycle.Go(ctx, func() { time.Sleep(10 * time.Millisecond) })
	lifecycle.Defer(ctx, func() { atomic.StoreInt64(&deferredRan, 1) })

	go func() {
		process, _ := os.FindProcess(syscall.Getpid())
		_ = process.Signal(syscall.SIGUSR1)
	}()

	err := lifecycle.Wait(ctx)
	if e, ok := err.(lifecycle.ErrSignal); ok {
		if !strings.Contains(e.Error(), "caught signal") {
			t.Error("unexpected error text")
		}
	} else {
		t.Errorf("unexpected error on signal interrupt: %v", err)
	}
	if atomic.LoadInt64(&deferredRan) != 1 {
		t.Error("signaled process did not run deferred func")
	}
	if dur := time.Since(start); dur > 200*time.Millisecond {
		t.Errorf("func ran for more than 200ms: %v", dur)
	}
}

func TestIgnoreSignals(t *testing.T) {
	const timeout = 5 * time.Millisecond
	ctx := lifecycle.New(
		context.Background(),
		lifecycle.WithTimeout(timeout),
		lifecycle.WithSignals(),
	)

	lifecycle.Defer(ctx, func() { time.Sleep(100 * time.Millisecond) })

	start := time.Now()
	go func() {
		process, _ := os.FindProcess(syscall.Getpid())
		_ = process.Signal(syscall.SIGUSR1)
	}()

	if err := lifecycle.Wait(ctx); err != context.DeadlineExceeded {
		t.Fatalf("expected deadline exceeded, got: %v", err)
	}

	if time.Since(start) < timeout {
		t.Fatalf("did not ignore signals")
	}
}

func TestRecover(t *testing.T) {
	ctx := lifecycle.New(context.Background())
	err := fmt.Errorf("test panic")

	lifecycle.Go(ctx, func() { panic(err) })

	defer func() {
		if r := recover(); r == nil {
			t.Error("did not panic")
		} else if r != err {
			t.Error("unexpected panic")
		}
	}()

	_ = lifecycle.Wait(ctx)
}

func TestDeferRecover(t *testing.T) {
	ctx := lifecycle.New(context.Background())
	err := fmt.Errorf("test panic")

	lifecycle.Defer(ctx, func() { panic(err) })

	defer func() {
		if r := recover(); r == nil {
			t.Error("did not panic")
		} else if r != err {
			t.Error("unexpected panic")
		}
	}()

	_ = lifecycle.Wait(ctx)
}

func TestExists(t *testing.T) {
	if lifecycle.Exists(context.Background()) {
		t.Error("expected Exists to return false for plain context")
	}
	ctx := lifecycle.New(context.Background())
	if !lifecycle.Exists(ctx) {
		t.Error("expected Exists to return true for lifecycle context")
	}
}

func TestGoErrNilFunc(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	lifecycle.GoErr(ctx, nil)
	err := lifecycle.Wait(ctx)
	if err != nil {
		t.Fatalf("unexpected error with nil func: %v", err)
	}
}

func TestGoCtxErr(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	var ran int64
	lifecycle.GoCtxErr(ctx, func(ctx context.Context) error {
		atomic.StoreInt64(&ran, 1)
		return nil
	})
	err := lifecycle.Wait(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt64(&ran) != 1 {
		t.Error("GoCtxErr did not run registered routine")
	}
}

func TestGoCtxErrReceivesContext(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	var gotCtx int64
	lifecycle.GoCtxErr(ctx, func(ctx context.Context) error {
		if lifecycle.Exists(ctx) {
			atomic.StoreInt64(&gotCtx, 1)
		}
		return nil
	})
	err := lifecycle.Wait(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt64(&gotCtx) != 1 {
		t.Error("GoCtxErr did not pass lifecycle context to function")
	}
}

func TestGoCtxErrReturnsError(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	lifecycle.GoCtxErr(ctx, func(ctx context.Context) error {
		return errors.New("ctx error")
	})
	err := lifecycle.Wait(ctx)
	if err == nil {
		t.Fatal("expected error but got nil")
	}
	if err.Error() != "ctx error" {
		t.Fatalf("expected \"ctx error\" but got: %v", err)
	}
}

func TestGoCtxErrNilFunc(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	lifecycle.GoCtxErr(ctx, nil)
	err := lifecycle.Wait(ctx)
	if err != nil {
		t.Fatalf("unexpected error with nil func: %v", err)
	}
}

func TestDeferCtx(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	var ran int64
	lifecycle.DeferCtx(ctx, func(ctx context.Context) {
		atomic.StoreInt64(&ran, 1)
	})
	err := lifecycle.Wait(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt64(&ran) != 1 {
		t.Error("DeferCtx did not run deferred routine")
	}
}

func TestDeferCtxReceivesContext(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	var gotCtx int64
	lifecycle.DeferCtx(ctx, func(ctx context.Context) {
		if lifecycle.Exists(ctx) {
			atomic.StoreInt64(&gotCtx, 1)
		}
	})
	err := lifecycle.Wait(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt64(&gotCtx) != 1 {
		t.Error("DeferCtx did not pass lifecycle context to function")
	}
}

func TestDeferCtxNilFunc(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	lifecycle.DeferCtx(ctx, nil)
	err := lifecycle.Wait(ctx)
	if err != nil {
		t.Fatalf("unexpected error with nil func: %v", err)
	}
}

func TestDeferCtxErr(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	var ran int64
	lifecycle.DeferCtxErr(ctx, func(ctx context.Context) error {
		atomic.StoreInt64(&ran, 1)
		return nil
	})
	err := lifecycle.Wait(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt64(&ran) != 1 {
		t.Error("DeferCtxErr did not run deferred routine")
	}
}

func TestDeferCtxErrReceivesContext(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	var gotCtx int64
	lifecycle.DeferCtxErr(ctx, func(ctx context.Context) error {
		if lifecycle.Exists(ctx) {
			atomic.StoreInt64(&gotCtx, 1)
		}
		return nil
	})
	err := lifecycle.Wait(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt64(&gotCtx) != 1 {
		t.Error("DeferCtxErr did not pass lifecycle context to function")
	}
}

func TestDeferCtxErrReturnsError(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	lifecycle.DeferCtxErr(ctx, func(ctx context.Context) error {
		return errors.New("deferred ctx error")
	})
	err := lifecycle.Wait(ctx)
	if err == nil {
		t.Fatal("expected error but got nil")
	}
	if err.Error() != "deferred ctx error" {
		t.Fatalf("expected \"deferred ctx error\" but got: %v", err)
	}
}

func TestDeferCtxErrNilFunc(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	lifecycle.DeferCtxErr(ctx, nil)
	err := lifecycle.Wait(ctx)
	if err != nil {
		t.Fatalf("unexpected error with nil func: %v", err)
	}
}

func TestMultipleVariadicArgs(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	var a, b, c int64
	lifecycle.Go(ctx,
		func() { atomic.StoreInt64(&a, 1) },
		func() { atomic.StoreInt64(&b, 1) },
		func() { atomic.StoreInt64(&c, 1) },
	)
	err := lifecycle.Wait(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt64(&a) != 1 || atomic.LoadInt64(&b) != 1 || atomic.LoadInt64(&c) != 1 {
		t.Error("not all variadic funcs were executed")
	}
}

func TestGoNilFunc(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	lifecycle.Go(ctx, nil)
	err := lifecycle.Wait(ctx)
	if err != nil {
		t.Fatalf("unexpected error with nil func: %v", err)
	}
}

func TestDeferNilFunc(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	lifecycle.Defer(ctx, nil)
	err := lifecycle.Wait(ctx)
	if err != nil {
		t.Fatalf("unexpected error with nil func: %v", err)
	}
}

func TestDeferErrNilFunc(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	lifecycle.DeferErr(ctx, nil)
	err := lifecycle.Wait(ctx)
	if err != nil {
		t.Fatalf("unexpected error with nil func: %v", err)
	}
}

func TestConcurrentRegistration(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	var goCount, deferCount atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lifecycle.Go(ctx, func() { goCount.Add(1) })
			lifecycle.Defer(ctx, func() { deferCount.Add(1) })
		}()
	}
	wg.Wait()

	err := lifecycle.Wait(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v := goCount.Load(); v != 100 {
		t.Fatalf("expected 100 Go funcs to run, got %d", v)
	}
	if v := deferCount.Load(); v != 100 {
		t.Fatalf("expected 100 Defer funcs to run, got %d", v)
	}
}

func TestDeferredRunsWhilePrimaryStillRunning(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	primaryDone := make(chan struct{})
	deferredStarted := make(chan struct{})

	// A primary func that returns an error quickly, triggering deferred funcs,
	// plus a slow primary func still running.
	lifecycle.GoErr(ctx, func() error { return errors.New("trigger shutdown") })
	lifecycle.Go(ctx, func() {
		defer close(primaryDone)
		// Wait until the deferred func has started, proving concurrency.
		select {
		case <-deferredStarted:
		case <-time.After(2 * time.Second):
			t.Error("timed out waiting for deferred func to start")
		}
	})
	lifecycle.Defer(ctx, func() {
		close(deferredStarted)
	})

	err := lifecycle.Wait(ctx)
	if err == nil {
		t.Fatal("expected error")
	}
	<-primaryDone
}

func TestDeferredTimeoutSkipsRemaining(t *testing.T) {
	ctx := lifecycle.New(
		context.Background(),
		lifecycle.WithTimeout(50*time.Millisecond),
	)

	var firstRan atomic.Int64

	// Registered first, so runs second (LIFO). Should be skipped by timeout.
	lifecycle.Defer(ctx, func() { firstRan.Store(1) })

	// Registered second, so runs first. Blocks longer than timeout.
	lifecycle.Defer(ctx, func() { time.Sleep(200 * time.Millisecond) })

	err := lifecycle.Wait(ctx)
	if err != context.DeadlineExceeded {
		t.Fatalf("expected deadline exceeded, got: %v", err)
	}
	// Wait returns as soon as the timeout fires. The slow deferred func's
	// goroutine is still running in the background but the next deferred
	// func in the queue should have been skipped.
	if firstRan.Load() != 0 {
		t.Error("expected remaining deferred func to be skipped after timeout")
	}
}

func TestDeferCtxLIFOOrder(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	var order []int
	var mu sync.Mutex
	appendOrder := func(v int) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, v)
	}

	lifecycle.DeferCtx(ctx, func(ctx context.Context) { appendOrder(1) })
	lifecycle.DeferCtx(ctx, func(ctx context.Context) { appendOrder(2) })
	lifecycle.DeferCtx(ctx, func(ctx context.Context) { appendOrder(3) })

	err := lifecycle.Wait(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 3 || order[0] != 3 || order[1] != 2 || order[2] != 1 {
		t.Fatalf("expected LIFO order [3 2 1], got %v", order)
	}
}

func TestDoubleWaitPanics(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	err := lifecycle.Wait(ctx)
	if err != nil {
		t.Fatalf("unexpected error on first Wait: %v", err)
	}

	testPanic(t, func() {
		_ = lifecycle.Wait(ctx)
	})
}

func TestPostWaitGoPanics(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	err := lifecycle.Wait(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	testPanic(t, func() {
		lifecycle.Go(ctx, func() {})
	})
}

func TestMultiplePanics(t *testing.T) {
	ctx := lifecycle.New(context.Background())
	firstPanic := fmt.Errorf("first panic")
	secondPanic := fmt.Errorf("second panic")

	ready := make(chan struct{})

	lifecycle.Go(ctx, func() {
		<-ready
		panic(firstPanic)
	})
	lifecycle.Go(ctx, func() {
		<-ready
		panic(secondPanic)
	})
	close(ready)

	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		_ = lifecycle.Wait(ctx)
	}()

	if recovered == nil {
		t.Fatal("expected a panic from Wait")
	}
	// Which panic is propagated is nondeterministic; verify it is one of the two.
	if recovered != firstPanic && recovered != secondPanic {
		t.Fatalf("unexpected panic value: %v", recovered)
	}
}

func TestPrimaryAndDeferredErrors(t *testing.T) {
	ctx := lifecycle.New(context.Background())

	primaryErr := errors.New("primary error")
	deferredErr := errors.New("deferred error")

	lifecycle.GoErr(ctx, func() error { return primaryErr })
	lifecycle.DeferErr(ctx, func() error { return deferredErr })

	err := lifecycle.Wait(ctx)
	if err == nil {
		t.Fatal("expected error but got nil")
	}
	if !errors.Is(err, primaryErr) {
		t.Errorf("expected errors.Is(err, primaryErr) to be true, got false; err = %v", err)
	}
	if !errors.Is(err, deferredErr) {
		t.Errorf("expected errors.Is(err, deferredErr) to be true, got false; err = %v", err)
	}
}
