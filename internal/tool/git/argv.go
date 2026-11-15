package git

import (
	"fmt"
	"strings"
)

// valueKind says whether an option carries an argument.
type valueKind int

const (
	// noValue options are written bare. "--short=1" is a mistake rather than a
	// shortening, so the equals form is refused instead of quietly trimmed.
	noValue valueKind = iota
	// takesValue options need an argument, given either as "--flag=value" or as
	// the next argument.
	takesValue
	// optionalValue options accept an argument only in the equals form. Bare is
	// the common case (--porcelain) and attached is the variant
	// (--porcelain=v2); modelling them as two options would double the table.
	optionalValue
)

// option is one accepted flag.
type option struct {
	name  string
	value valueKind
}

// command is one read-only subcommand: the tool name it is registered under, the
// git subcommand it runs, the options it accepts, whether bare arguments are
// revisions, and what the truncation notice suggests instead of spilling the full
// output to a file.
//
// The hint is per command because "narrow it" means something different in each:
// a path for status and show, a path or a summary flag for diff, and a count for
// log.
type command struct {
	tool        string
	name        string
	description string
	options     []option
	// positionals allows bare arguments before "--": a revision such as HEAD~2,
	// a range, or a path git will disambiguate. status takes none of them, which
	// is why the flag is per command rather than global.
	positionals bool
	hint        string
}

// lookup finds an accepted option by its exact name.
func (c command) lookup(name string) (option, bool) {
	for _, candidate := range c.options {
		if candidate.name == name {
			return candidate, true
		}
	}
	return option{}, false
}

// deniedOptions are refused with a reason instead of the generic message. Every
// one of them turns a read into something else: --textconv and --ext-diff run the
// diff driver or external diff program named in repository configuration, -c and
// --config-env set configuration for the command, --exec-path points git at
// another binary directory, --output writes a file, and --upload-pack runs a
// program in place of the remote end.
//
// -c needs a word of explanation. Global options can only be written before the
// subcommand, and this tool always writes the subcommand first, so a -c that
// reached git here would be the combined-diff flag rather than a configuration
// override. It is still refused: one rule that says "no token from this family
// ever reaches git" is checkable, whereas "no token from this family in the
// position where it would mean that" depends on knowing git's parser, and the
// combined diff for a merge is not something a model needs.
var deniedOptions = map[string]string{
	"-c":            "it sets a configuration value for one command, and a config key is how git is pointed at a program",
	"--config-env":  "it sets a configuration value for one command, and a config key is how git is pointed at a program",
	"--exec-path":   "it points git at a different binary directory",
	"--textconv":    "it runs the diff driver named in repository configuration",
	"--ext-diff":    "it runs the external diff program named in repository configuration",
	"--output":      "it writes the result to a file, and these tools only read",
	"--upload-pack": "it runs a program in place of the remote end",
}

// buildArgs validates the caller's arguments and returns the argv tail to run
// after the subcommand.
//
// Everything it returns is either a whitelisted option or an argument that came
// after "--", which is the only place a leading dash is read as a path rather
// than as an option. Nothing is dropped: an argument this function does not
// understand is an error the model can read and correct, because silently
// ignoring "--textconv" would leave the caller believing it took effect.
func buildArgs(cmd command, args []string) ([]string, error) {
	out := make([]string, 0, len(args))
	paths := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if paths {
			if arg == "" {
				return nil, fmt.Errorf("git %s: empty path after --", cmd.name)
			}
			out = append(out, arg)
			continue
		}
		if arg == "--" {
			paths = true
			out = append(out, arg)
			continue
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			if !cmd.positionals {
				return nil, fmt.Errorf("git %s takes no arguments besides options; put a path after --", cmd.name)
			}
			out = append(out, arg)
			continue
		}

		name, value, attached := strings.Cut(arg, "=")
		if reason, denied := deniedOptions[name]; denied {
			return nil, fmt.Errorf("git %s refuses %s: %s", cmd.name, name, reason)
		}
		accepted, ok := cmd.lookup(name)
		if !ok {
			// "-n5" and "-U3" are how a short option with a value is usually
			// written, so the two-character prefix is tried before giving up. It
			// is passed through whole: git parses the attached form itself.
			if short, found := cmd.shortWithValue(name); found {
				// The value is inside the token ("-n5"), so this reads as the
				// attached form from here on and no next argument is consumed.
				accepted, ok, attached, value = short, true, true, name[2:]
			}
		}
		if !ok {
			return nil, fmt.Errorf("git %s does not accept %q; it runs read-only commands with a fixed option list", cmd.name, name)
		}
		switch accepted.value {
		case noValue:
			if attached {
				return nil, fmt.Errorf("git %s: %s does not take a value", cmd.name, name)
			}
			out = append(out, arg)
		case optionalValue:
			out = append(out, arg)
		case takesValue:
			if attached {
				if value == "" {
					return nil, fmt.Errorf("git %s: %s needs a value", cmd.name, name)
				}
				out = append(out, arg)
				continue
			}
			if i+1 >= len(args) {
				return nil, fmt.Errorf("git %s: %s needs a value", cmd.name, name)
			}
			i++
			out = append(out, arg, args[i])
		}
	}
	return out, nil
}

// shortWithValue reports whether a token like "-n5" is a known short option with
// its value attached. Only short options that take a value are candidates: "-sb"
// is not "--short --branch" squashed together, and pretending it were would be
// inventing a spelling git does not accept.
func (c command) shortWithValue(token string) (option, bool) {
	if strings.HasPrefix(token, "--") || len(token) <= 2 {
		return option{}, false
	}
	accepted, ok := c.lookup(token[:2])
	if !ok || accepted.value == noValue {
		return option{}, false
	}
	return accepted, true
}
