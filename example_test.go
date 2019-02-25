package lifecycle_test

import (
	"context"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/joshuarubin/lifecycle"
)

func Example() {
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

	lifecycle.GoErr(ctx, func() error {
		return srv.ListenAndServe()
	})

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
}
