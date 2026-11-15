//go:build !darwin && !linux

package sandbox

import "runtime"

// defaultExecutor reports that this platform has no executor yet.
//
// A policy that compiles but cannot be applied is reported as an error on every
// command rather than as a quieter "run it unconfined": a sandbox that silently
// disappears when it is unavailable is worse than no sandbox, because the config
// says otherwise.
func defaultExecutor() (string, string) {
	return "", "no executor on " + runtime.GOOS + "; the policy compiles, the executor does not exist yet"
}
