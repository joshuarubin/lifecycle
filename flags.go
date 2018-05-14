package lifecycle

import "github.com/spf13/pflag"

// Flags that will configure the handler when parsed
func (m *manager) Flags() (f *pflag.FlagSet) {
	f = pflag.NewFlagSet("shutdown", pflag.ContinueOnError)
	f.DurationVar(&m.timeout, "shutdown-timeout", m.timeout,
		"Time to wait for routines to complete after initiating a shutdown.")
	return
}
