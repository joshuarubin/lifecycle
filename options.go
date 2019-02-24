package lifecycle

import (
	"os"
	"time"
)

// An Option is used to configure the lifecycle manager
type Option func(*manager)

// WithTimeout sets an upper limit for how much time Handle will wait to return.
// After the Go funcs finish, if WhenDone was used, or after a signal is
// received if WhenSignaled was used, this timer starts. From that point, Handle
// will return if any Go or Defer function takes longer than this value.
func WithTimeout(val time.Duration) Option {
	return func(o *manager) {
		o.timeout = val
	}
}

// WithSignals causes Handle to wait for Go funcs to finish, if WhenDone was
// used or until a signal is received. The signals it will wait for can be
// defined with WithSigs or will default to DefaultSignals
func WithSignals(val ...os.Signal) Option {
	return func(o *manager) {
		o.sigs = val
	}
}

// WithRecover provides a function that will be called after a panic. It will be
// passed the value returned by recover(). This provides a mechanism for panics
// to be recorded somewhere. After calling fn, the value will be re-panicked.
func WithRecover(fn func(interface{})) Option {
	return func(o *manager) {
		o.recover = fn
	}
}
