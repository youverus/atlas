//go:build !unix

package shell

import (
	"errors"

	"pi-go/internal/tool"
)

// background refuses on this platform.
//
// The schema drops run_in_background whenever there is no recorder, so a model
// here never sees the argument. This path exists for an argument that arrives
// anyway: refusing is the only honest answer, and running it in the foreground
// would report a blocking command as a started one.
func (t *bashTool) background(call tool.Call, spec backgroundSpec) (tool.Result, error) {
	return tool.Result{}, errors.New("bash: background commands are not supported on this platform")
}
