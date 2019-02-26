package lifecycle_test

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/joshuarubin/lifecycle"
)

func Example() {
	// This is only to ensure that the example completes
	const timeout = 10 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// At the top of your application
	ctx = lifecycle.New(
		ctx,
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

	var start, finish time.Time

	lifecycle.GoErr(ctx, func() error {
		start = time.Now()
		return srv.ListenAndServe()
	})

	lifecycle.DeferErr(ctx, func() error {
		finish = time.Now()
		fmt.Println("shutting down http server")
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
	if err := lifecycle.Wait(ctx); err != nil && err != context.DeadlineExceeded {
		log.Fatal(err)
	}

	// This is just to show that the server will run for at least `timeout`
	// before shutting down
	if finish.Sub(start) < timeout {
		log.Fatal("didn't wait long enough to shutdown")
	}

	// Output:
	// shutting down http server
}
