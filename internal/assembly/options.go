package assembly

// Options is one assembled kernel: what the config file says, and what the
// caller asked to override.
//
// It is a struct rather than a list of arguments because three front ends pass
// it: the command line builds one from flags, a window builds one from nothing
// but the config, and a test builds one by hand. The fields therefore describe
// intent ("run offline") rather than a particular way of spelling a flag.
type Options struct {
	// ConfigPath names the config file. Empty means the default under the
	// atlas home, which is also the path a first run writes a template to.
	ConfigPath string

	// Model selects a model: "provider/model" or a unique model id. Empty
	// takes the config's default, which is the first provider it declares.
	Model string

	// Thinking names a level, which is fitted to what the chosen model
	// offers. Empty takes the config's own default.
	Thinking string

	// Workdir is the directory tools run in and sessions are recorded
	// against. Empty means the process working directory.
	Workdir string

	// PermissionMode overrides permission.mode for this run. Empty takes the
	// config's mode.
	PermissionMode string

	// SystemFile replaces the built-in instructions; the project's context
	// files are still read.
	SystemFile string

	// NoContextFiles and NoSkills are the two escapes that make "nothing was
	// added to the prompt" a testable claim.
	NoContextFiles bool
	NoSkills       bool

	// SessionPath opens one session file, ContinueLast follows the newest for
	// the directory, and SessionDir overrides where sessions are written.
	SessionPath  string
	ContinueLast bool
	SessionDir   string

	// Ask answers a tool that needs a human. Nil means this front end cannot
	// ask, and the gate then denies with "no reader" rather than approving in
	// silence — a window with no approval box must not be a window with no
	// permission system.
	Ask func(Ask) Answer

	// Version is the identity a server sees in a log line.
	Version string

	// NoMCP skips connecting the servers this config declares. It exists for a
	// front end that wants a conversation without spawning child processes.
	NoMCP bool
}

// Ask is one question about one tool call.
//
// It is this package's own shape rather than the terminal layer's, because the
// permission ladder is a kernel concern: a window asks the same question through
// a blocking POST that the terminal asks through a keypress. The subject is the
// sentence shown to the reader, and the tool name is what a "yes, and stop
// asking" answer is remembered against.
type Ask struct {
	Subject string
	Tool    string
}

// Answer is what the reader said. Always is the third state: yes, and stop
// asking for this tool for the rest of this session.
type Answer struct {
	Allowed bool
	Always  bool
}
