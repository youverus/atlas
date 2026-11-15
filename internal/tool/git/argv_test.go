package git

import (
	"strings"
	"testing"
)

// The whitelist is the entire read-only guarantee, so these cases are the
// contract rather than examples: each one names an argv that must either reach
// git unchanged or be refused with a reason.
func TestBuildArgsAllowsTheWhitelist(t *testing.T) {
	cases := []struct {
		name string
		cmd  command
		args []string
		want []string
	}{
		{
			name: "status short and branch",
			cmd:  statusCommand,
			args: []string{"--short", "-b"},
			want: []string{"--short", "-b"},
		},
		{
			name: "porcelain takes an optional value",
			cmd:  statusCommand,
			args: []string{"--porcelain=v2"},
			want: []string{"--porcelain=v2"},
		},
		{
			name: "a value written apart from its flag",
			cmd:  logCommand,
			args: []string{"-n", "5", "--oneline"},
			want: []string{"-n", "5", "--oneline"},
		},
		{
			name: "a short flag with its value attached",
			cmd:  logCommand,
			args: []string{"-n5"},
			want: []string{"-n5"},
		},
		{
			name: "diff takes a revision",
			cmd:  diffCommand,
			args: []string{"HEAD~1"},
			want: []string{"HEAD~1"},
		},
		{
			name: "a range is a revision too",
			cmd:  logCommand,
			args: []string{"main..HEAD", "--stat"},
			want: []string{"main..HEAD", "--stat"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := buildArgs(testCase.cmd, testCase.args)
			if err != nil {
				t.Fatalf("buildArgs: %v", err)
			}
			if strings.Join(got, "\x00") != strings.Join(testCase.want, "\x00") {
				t.Fatalf("argv = %q, want %q", got, testCase.want)
			}
		})
	}
}

// Everything after "--" is a path, including things that look like options. This
// is the only way to name a file called "-draft.md", and it is why the scanner
// keeps a separate mode instead of testing each token on its own.
func TestBuildArgsReadsPathsAfterTheSeparator(t *testing.T) {
	got, err := buildArgs(diffCommand, []string{"--stat", "--", "--not-an-option", "-x"})
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	want := []string{"--stat", "--", "--not-an-option", "-x"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

// A refusal has to say why. "--textconv" and its neighbours are the whole reason
// this package has a whitelist instead of passing arguments through, so they get
// their own message rather than the generic one.
func TestBuildArgsRefusesTheOptionsThatRunPrograms(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"textconv", []string{"--textconv"}, "runs the diff driver"},
		{"ext diff", []string{"--ext-diff"}, "runs the external diff program"},
		{"config", []string{"-c", "core.pager=less"}, "sets a configuration value"},
		{"config env", []string{"--config-env=K=V"}, "sets a configuration value"},
		{"exec path", []string{"--exec-path=/tmp/bin"}, "points git at a different binary directory"},
		{"output", []string{"--output=/tmp/x"}, "writes the result to a file"},
		{"upload pack", []string{"--upload-pack=/tmp/x"}, "runs a program in place of the remote end"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := buildArgs(diffCommand, testCase.args)
			if err == nil {
				t.Fatalf("buildArgs accepted %q; it must be refused", testCase.args)
			}
			if !strings.Contains(err.Error(), "refuses") || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %q, want it to name the reason %q", err, testCase.want)
			}
		})
	}
}

// An argument outside the whitelist is an error, never a silent drop: a caller
// whose option was ignored would believe it took effect.
func TestBuildArgsRefusesWhatIsNotOnTheList(t *testing.T) {
	cases := []struct {
		name string
		cmd  command
		args []string
		want string
	}{
		{"unknown long option", diffCommand, []string{"--binary"}, `does not accept "--binary"`},
		{"option from another subcommand", statusCommand, []string{"--stat"}, `does not accept "--stat"`},
		{"bare argument for status", statusCommand, []string{"src/"}, "put a path after --"},
		{"value missing", logCommand, []string{"-n"}, "needs a value"},
		{"empty value", logCommand, []string{"--max-count="}, "needs a value"},
		{"value on a flag that takes none", statusCommand, []string{"--short=1"}, "does not take a value"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := buildArgs(testCase.cmd, testCase.args)
			if err == nil {
				t.Fatalf("buildArgs accepted %q", testCase.args)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %q, want it to contain %q", err, testCase.want)
			}
		})
	}
}

// Squashing several flags into one token is not a spelling git accepts, so it is
// not invented here either. Only a short option that takes a value may carry it
// attached.
func TestBuildArgsDoesNotInventBundledShortFlags(t *testing.T) {
	if _, err := buildArgs(statusCommand, []string{"-sb"}); err == nil {
		t.Fatal("buildArgs accepted -sb; bundled short flags are not a git spelling")
	}
}

// Color is never wanted and is not something the caller has to remember. It is
// switched off in the environment because `git status` has no color option, so a
// flag would have to be per command and the whitelist would start describing
// four parsers.
func TestGitEnvTurnsColorOff(t *testing.T) {
	joined := strings.Join(gitEnv(), "\n")
	for _, want := range []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=color.ui",
		"GIT_CONFIG_VALUE_0=false",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("env is missing %q", want)
		}
	}
}

// A caller that already passes configuration this way must not end up with two
// counts in one environment: git would read whichever came first.
func TestGitEnvDropsInheritedConfiguration(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_0", "core.pager")
	t.Setenv("GIT_CONFIG_VALUE_0", "less")

	count := 0
	for _, entry := range gitEnv() {
		if strings.HasPrefix(entry, "GIT_CONFIG_COUNT=") {
			count++
		}
		if strings.HasPrefix(entry, "GIT_CONFIG_KEY_0=") && entry != "GIT_CONFIG_KEY_0=color.ui" {
			t.Fatalf("inherited key survived: %q", entry)
		}
	}
	if count != 1 {
		t.Fatalf("GIT_CONFIG_COUNT appears %d times in the environment", count)
	}
}
