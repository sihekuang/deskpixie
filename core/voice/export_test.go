package voice

// WithPlayHook lets external tests see every playback enqueue (n samples) and flush.
func WithPlayHook(cfg RuntimeConfig, hook func(n int, flush bool)) RuntimeConfig {
	cfg.playHook = hook
	return cfg
}
