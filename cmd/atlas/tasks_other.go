//go:build !unix

package main

import (
	"pi-go/internal/session"
	"pi-go/internal/tool"
)

// tasksFor reports no capability on this platform.
//
// There is no detached runner here, so there is nothing to record and nothing to
// read back. Nil for both is what keeps run_in_background out of the bash schema
// and task_query out of the tool list: a model on this platform never sees a
// feature that could only fail.
func tasksFor(manager *session.Manager, spill *tool.Spill) taskWiring {
	return taskWiring{}
}
