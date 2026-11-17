//go:build !unix

package assembly

import (
	"pi-go/internal/session"
	"pi-go/internal/tool"
)

// TasksFor reports no capability on this platform.
//
// There is no detached runner here, so there is nothing to record and nothing to
// read back. Nil for both is what keeps run_in_background out of the bash schema
// and task_query out of the tool list: a model on this platform never sees a
// feature that could only fail.
func TasksFor(manager *session.Manager, spill *tool.Spill) TaskWiring {
	return TaskWiring{}
}
