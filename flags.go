package lifecycle

import (
	"flag"
)

// Flags that will configure the handler when parsed
func (m *manager) Flags(prefix string) (f *flag.FlagSet) {
	f = flag.NewFlagSet("shutdown", flag.ContinueOnError)
	f.Duration(prefix+"shutdown-timeout", m.timeout,
		"Time to wait for routines to complete after initiating a shutdown.")
	return
}
