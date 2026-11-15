// Package git implements four read-only git tools: status, diff, log and show.
//
// They are read-only by construction rather than by promise. Every argument the
// caller supplies is checked against a short per-subcommand whitelist, and the
// options that would turn a read into a way to run another program are refused
// with a reason (see deniedOptions). A model that wants to commit, push or check
// out asks for bash; these tools exist for the opposite reason — to read a
// repository without giving a command the power to write to it.
//
// The tools are not confined by the sandbox. The sandbox's boundary is "which
// command line did the user approve", and the approval for these tools is the
// whitelist itself: no argument reaches git that could name an external program.
// GIT_OPTIONAL_LOCKS=0 closes the one write a read otherwise performs, which is
// status refreshing the index.
//
// Output is truncated at the head with a notice saying how to narrow the request.
// Nothing is spilled to a file: every one of these commands can be asked for less
// (a count, a path, a summary flag), so the second attempt is better than the
// first, and that is the rule the output-file work settled on.
package git

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

// Options configures the tool set.
type Options struct {
	// Cwd is the directory git runs in, and therefore the repository it reads.
	Cwd string
	// Path overrides the git binary. Tests use it; production leaves it empty and
	// takes what PATH offers.
	Path string
}

// Tools returns the git tools in the order they are registered.
//
// A machine without git gets no tools at all instead of four that always fail:
// a tool that cannot work is worse than a tool the model cannot see, because the
// first one costs a turn every time it is tried.
func Tools(options Options) []tool.Tool {
	binary := options.Path
	if binary == "" {
		found, err := exec.LookPath("git")
		if err != nil {
			return nil
		}
		binary = found
	}
	set := &shared{cwd: options.Cwd, binary: binary}
	return []tool.Tool{
		cmdTool{set: set, cmd: statusCommand},
		cmdTool{set: set, cmd: diffCommand},
		cmdTool{set: set, cmd: logCommand},
		cmdTool{set: set, cmd: showCommand},
	}
}

// shared is the state every git tool needs: where the repository is, and which
// binary reads it.
type shared struct {
	cwd    string
	binary string
}

type details struct {
	Command    string           `json:"command"`
	ExitCode   int              `json:"exitCode,omitempty"`
	Truncation *tool.Truncation `json:"truncation,omitempty"`
}

// run executes one subcommand and turns its output into a tool result.
func (s *shared) run(ctx context.Context, cmd command, args []string) (tool.Result, error) {
	tail, err := buildArgs(cmd, args)
	if err != nil {
		return tool.Result{}, err
	}
	// --no-pager goes before the subcommand because that is where git accepts it.
	argv := append([]string{"--no-pager", cmd.name}, tail...)
	process := exec.CommandContext(ctx, s.binary, argv...)
	process.Dir = s.cwd
	process.Env = gitEnv()

	combined, runErr := process.CombinedOutput()
	exitCode := 0
	if runErr != nil {
		// A cancelled context kills the process, and the kill is not the failure:
		// reporting the signal would tell the agent the turn ended for a reason it
		// can do nothing about.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return tool.Result{}, ctxErr
		}
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			return tool.Result{}, fmt.Errorf("git %s: %w", cmd.name, runErr)
		}
		exitCode = exitErr.ExitCode()
	}

	text := strings.TrimRight(string(combined), "\n")
	if text == "" {
		text = "(no output)"
	}

	truncation := tool.TruncateHead(text, tool.Limits{})
	output := truncation.Content
	details := details{Command: "git " + cmd.name, ExitCode: exitCode}

	var notices []string
	if truncation.Truncated {
		notices = append(notices, truncationNotice(cmd, truncation))
		truncationCopy := truncation
		details.Truncation = &truncationCopy
	}
	if exitCode != 0 {
		// git's own message is above this line: it says what went wrong, and this
		// says that the call failed. Both are needed, in that order.
		notices = append(notices, fmt.Sprintf("git %s exited with code %d", cmd.name, exitCode))
	}
	if len(notices) > 0 {
		output = appendStatus(output, strings.Join(notices, "\n"))
	}

	result := tool.Result{
		Content: []wire.Content{wire.TextContent(output)},
		IsError: exitCode != 0,
	}
	if details.Truncation != nil || details.ExitCode != 0 {
		encoded, marshalErr := json.Marshal(details)
		if marshalErr != nil {
			return tool.Result{}, marshalErr
		}
		result.Details = encoded
	}
	return result, nil
}

// gitEnv builds the environment git runs in.
//
// Color is switched off through the environment rather than with a flag, because
// `git status` has no color option: a flag that three of the four commands accept
// and one rejects would make one whitelist describe four different parsers.
// GIT_CONFIG_COUNT is the documented way to pass a configuration value without
// `-c`, which these tools refuse.
//
// Any configuration the caller pushed through the same mechanism is dropped
// first. git reads the count from the environment and then expects exactly that
// many key/value pairs; two counts in one environment would leave it reading
// whichever came first.
func gitEnv() []string {
	inherited := os.Environ()
	env := make([]string, 0, len(inherited)+7)
	for _, entry := range inherited {
		if strings.HasPrefix(entry, "GIT_CONFIG_COUNT=") ||
			strings.HasPrefix(entry, "GIT_CONFIG_KEY_") ||
			strings.HasPrefix(entry, "GIT_CONFIG_VALUE_") {
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=color.ui",
		"GIT_CONFIG_VALUE_0=false",
		// Without this, status refreshes and writes the index. The tool would then
		// be a write, which is the one thing it promises not to be.
		"GIT_OPTIONAL_LOCKS=0",
		// A credential prompt reads the terminal, and there is no terminal here.
		"GIT_TERMINAL_PROMPT=0",
		// Belt and braces with --no-pager: a configured pager would otherwise sit
		// between git and the pipe we read.
		"GIT_PAGER=cat",
	)
}

// truncationNotice says how much was shown and how to ask for less. It never
// names a file: these tools do not spill, so offering a path would be a lie.
func truncationNotice(cmd command, truncation tool.Truncation) string {
	var shown string
	if truncation.TruncatedBy == "bytes" {
		shown = fmt.Sprintf("Showing the first %s of %s",
			tool.FormatSize(truncation.OutputBytes), tool.FormatSize(truncation.TotalBytes))
	} else {
		shown = fmt.Sprintf("Showing the first %d of %d lines",
			truncation.OutputLines, truncation.TotalLines)
	}
	return fmt.Sprintf("%s. Narrow it with %s.", shown, cmd.hint)
}

// appendStatus adds a block after the output, separated the way the bash tool
// separates its own status lines, so a failure reads the same in both tools.
func appendStatus(text, status string) string {
	if text == "" {
		return status
	}
	return text + "\n\n" + status
}
