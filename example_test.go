package lifecycle_test

import (
	"context"
	"log"
	"time"

	"github.com/joshuarubin/lifecycle"
)

func Example() {
	// At the top of your application
	ctx := lifecycle.New(
		context.Background(),
		lifecycle.WithTimeout(30*time.Second),
	)

	funcErr := func() error {
		// do stuff
		return nil
	}

	funcCtx := func(ctx context.Context) {
		done := make(chan struct{})
		go func() {
			// do stuff
			close(done)
		}()

		select {
		case <-done:
		case <-ctx.Done():
		}
	}

	funcCtxErr := func(ctx context.Context) error {
		done := make(chan struct{})
		go func() {
			// do stuff
			close(done)
		}()

		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	{ // Then you execute things that it will manage
		lifecycle.Go(ctx, func() {
			// do stuff
		})
		lifecycle.Go(ctx, funcErr)
		lifecycle.Go(ctx, funcCtx)
		lifecycle.Go(ctx, funcCtxErr)
	}

	{ // You can also add cleanup tasks that only get run on shutdown
		lifecycle.Defer(ctx, func() {
			// do cleanup stuff
		})
		lifecycle.Defer(ctx, funcErr)
		lifecycle.Defer(ctx, funcCtx)
		lifecycle.Defer(ctx, funcCtxErr)
	}

	// Then at the end of main(), or run() or however your application operates
	//
	// The returned err is the first non-nil error returned by any func
	// registered with Go or Defer, otherwise nil.
	if err := lifecycle.Wait(ctx); err != nil {
		log.Fatal(err)
	}
}
