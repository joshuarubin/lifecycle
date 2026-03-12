package lifecycle

import (
	"os"
	"time"
)

// An Option is used to configure the lifecycle manager
type Option func(*manager)

// WithTimeout sets an upper limit for how much time Wait will wait for the
// deferred phase to complete. The timer starts once the primary Go funcs are
// signaled to stop (i.e., the lifecycle context is canceled). The timeout
// covers both the time for primary Go funcs to finish responding to context
// cancellation and the execution of all Defer funcs.
func WithTimeout(val time.Duration) Option {
	return func(o *manager) {
		o.timeout = val
	}
}

// WithSignals configures which signals will cause the lifecycle to begin its
// shutdown sequence. Pass no arguments to disable signal handling. Defaults to
// syscall.SIGINT and syscall.SIGTERM.
func WithSignals(val ...os.Signal) Option {
	return func(o *manager) {
		o.sigs = val
	}
}
