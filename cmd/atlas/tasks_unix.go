//go:build unix

package main

import (
	"os"

	"pi-go/internal/session"
	"pi-go/internal/task"
	"pi-go/internal/tool"
	"pi-go/internal/tool/tasks"
)

// taskLogSuffix names the file a background command writes into. It is spelled
// here rather than in the task package because it describes where this front end
// keeps files, and the package has no opinion about that.
const taskLogSuffix = ".log"

// tasksFor builds the record service and the tools that read it.
//
// Both come back together because they are the same capability seen twice: the
// service is where bash reports a detached command, and the tools are how a later
// call finds it. A platform that can run neither returns both as nil, which is
// what removes the argument and the tools from the model's view.
func tasksFor(manager *session.Manager, spill *tool.Spill) taskWiring {
	service := task.New(task.Options{
		// Asked per task rather than captured: /resume changes the id, and a
		// task recorded under the previous conversation would be invisible to
		// the one that is running.
		OwnerSession: manager.ID,
		// The records live beside the session, so deleting a conversation takes
		// its task history and its output files with it.
		Dir: manager.Dir,
		Create: func(prefix string) (*os.File, error) {
			return spill.Create(prefix, taskLogSuffix)
		},
	})
	return taskWiring{service: service, tools: tasks.Tools(service)}
}
