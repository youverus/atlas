package git

import (
	"context"
	"encoding/json"

	"pi-go/internal/tool"
)

// The four commands are separate tools rather than one tool with a subcommand
// field. The reason is the same one that keeps websearch and webfetch apart: a
// single tool would need the union of four option lists and a parameter that
// only makes sense for one of them at a time, and a model handling conditional
// parameters is a model inventing arguments.
var (
	statusCommand = command{
		tool: "git_status",
		name: "status",
		description: "Show the working tree status: the current branch, and which files are staged, modified or untracked. " +
			"Read-only; it never writes to the repository. " +
			`Options such as --short, --branch or --porcelain are accepted, and a path goes after "--". ` +
			"Output is truncated to the first 2000 lines or 50KB.",
		options: []option{
			{"-s", noValue}, {"--short", noValue},
			{"-b", noValue}, {"--branch", noValue},
			{"--porcelain", optionalValue},
			{"--long", noValue},
			// Both of these take their mode in the equals form only
			// (--untracked-files[=<mode>]); written apart from the flag git reads
			// the mode as a pathspec, so the table says optional.
			{"--ignored", optionalValue},
			{"--untracked-files", optionalValue},
			{"--no-renames", noValue},
		},
		hint: `a path after "--"`,
	}

	diffCommand = command{
		tool: "git_diff",
		name: "diff",
		description: "Show changes between the working tree, the index and a revision. " +
			"Read-only; it never writes to the repository or to a file. " +
			`Revisions such as HEAD~1 or a two-dot range are accepted, a path goes after "--", ` +
			"and options such as --stat, --name-only, --cached and -U3 are accepted. " +
			"Output is truncated to the first 2000 lines or 50KB.",
		options: []option{
			{"--stat", optionalValue},
			{"--numstat", noValue},
			{"--shortstat", noValue},
			{"--name-only", noValue},
			{"--name-status", noValue},
			{"--cached", noValue}, {"--staged", noValue},
			{"-U", takesValue}, {"--unified", takesValue},
			{"-M", optionalValue}, {"--find-renames", optionalValue},
			{"-w", noValue}, {"--ignore-all-space", noValue},
			{"--check", noValue},
		},
		positionals: true,
		hint:        `a path after "--", or --stat / --name-only`,
	}

	logCommand = command{
		tool: "git_log",
		name: "log",
		description: "Show commit history, newest first. " +
			"Read-only; it never writes to the repository. " +
			`Revisions and ranges are accepted, a path goes after "--", and options such as ` +
			"-n 20, --oneline, --stat, --graph, --author and --since are accepted. " +
			"Output is truncated to the first 2000 lines or 50KB, so pass a count when the history is long.",
		options: []option{
			{"-n", takesValue}, {"--max-count", takesValue},
			{"--oneline", noValue},
			{"--stat", optionalValue},
			{"--graph", noValue},
			{"--decorate", optionalValue},
			{"--author", takesValue},
			{"--since", takesValue}, {"--until", takesValue},
			{"--no-merges", noValue},
			{"--first-parent", noValue},
			{"--reverse", noValue},
			{"--format", takesValue}, {"--pretty", takesValue},
			{"-p", noValue}, {"--patch", noValue},
			{"--name-only", noValue}, {"--name-status", noValue},
		},
		positionals: true,
		hint:        `-n <count>, or a path after "--"`,
	}

	showCommand = command{
		tool: "git_show",
		name: "show",
		description: "Show one commit, tag or blob with its diff. " +
			"Read-only; it never writes to the repository or to a file. " +
			`A revision such as HEAD or a tag name is accepted, a path goes after "--", ` +
			"and options such as --stat, --name-only, -s and -U3 are accepted. " +
			"Output is truncated to the first 2000 lines or 50KB.",
		options: []option{
			{"--stat", optionalValue},
			{"--numstat", noValue}, {"--shortstat", noValue},
			{"--name-only", noValue}, {"--name-status", noValue},
			{"-U", takesValue}, {"--unified", takesValue},
			{"--format", takesValue}, {"--pretty", takesValue},
			{"-s", noValue}, {"--no-patch", noValue},
			{"-w", noValue}, {"--ignore-all-space", noValue},
			{"-M", optionalValue}, {"--find-renames", optionalValue},
		},
		positionals: true,
		hint:        `a path after "--", or --stat / --name-only`,
	}
)

// argsParameters is the schema every git tool declares. One array rather than a
// field per option: the options are the interface, the whitelist is the contract,
// and a field per option would have to be kept in step with a table that already
// exists in one place.
const argsParameters = `{
  "type": "object",
  "properties": {
    "args": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Options and revisions, one per element, exactly as they would follow the subcommand. Everything after \"--\" is read as a path, never as an option."
    }
  }
}`

type cmdTool struct {
	set *shared
	cmd command
}

func (t cmdTool) Name() string                { return t.cmd.tool }
func (t cmdTool) Label() string               { return t.cmd.tool }
func (t cmdTool) Description() string         { return t.cmd.description }
func (t cmdTool) Parameters() json.RawMessage { return json.RawMessage(argsParameters) }

type argsRequest struct {
	Args []string `json:"args"`
}

func (t cmdTool) Execute(ctx context.Context, call tool.Call, _ func(tool.Partial)) (tool.Result, error) {
	var request argsRequest
	if err := tool.Decode(call.Arguments, &request); err != nil {
		return tool.Result{}, err
	}
	return t.set.run(ctx, t.cmd, request.Args)
}
