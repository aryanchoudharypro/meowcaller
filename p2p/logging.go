package p2p

import "github.com/rs/zerolog"

// Option configures optional, non-behavioral aspects of a P2P connection —
// currently the diagnostic logger. The zero configuration logs nothing.
type Option func(*config)

type config struct {
	log zerolog.Logger
}

func resolveConfig(opts []Option) config {
	c := config{log: zerolog.Nop()}
	for _, opt := range opts {
		opt(&c)
	}
	return c
}

// WithLogger sets the zerolog logger for debug/trace diagnostics.
func WithLogger(l zerolog.Logger) Option {
	return func(c *config) { c.log = l }
}
