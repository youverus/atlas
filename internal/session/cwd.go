package session

import "os"

// MissingCwd reports the recorded working directory when it no longer exists.
// A resumed session keeps its header cwd for the tools to run in, so a deleted
// directory would otherwise surface as a failure inside the first tool call.
//
// It only answers the question; what to do about it belongs to the caller,
// because only the caller knows whether it can point the session somewhere else
// (-cwd) or has to refuse.
func (m *Manager) MissingCwd() (string, bool) {
	if m.cwd == "" {
		return "", false
	}
	if _, err := os.Stat(m.cwd); os.IsNotExist(err) {
		return m.cwd, true
	}
	return "", false
}
