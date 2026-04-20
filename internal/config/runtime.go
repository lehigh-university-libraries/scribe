package config

import "sync"

// Runtime bundles config + secrets loaded at startup. A single instance is
// initialized in main() via Init and then read by call sites throughout the
// request path.
type Runtime struct {
	Config  Config
	Secrets Secrets
}

var (
	runtimeMu sync.RWMutex
	runtime   *Runtime
)

// Init stores the runtime bundle for later retrieval via Get. Must be called
// once at startup before any handler or request-path code runs.
func Init(rt Runtime) {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	runtime = &rt
}

// Get returns the active runtime bundle. Callers should treat the returned
// value as read-only. Returns a zero-value Runtime if Init has not been called
// (e.g. in isolated unit tests); consumers should fall back to their own
// defaults in that case.
func Get() Runtime {
	runtimeMu.RLock()
	defer runtimeMu.RUnlock()
	if runtime == nil {
		return Runtime{}
	}
	return *runtime
}
