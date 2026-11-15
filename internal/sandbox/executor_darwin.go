//go:build darwin

package sandbox

// defaultExecutor names the program that applies a Seatbelt profile.
//
// sandbox-exec has carried a deprecation notice since 2013 and still ships on
// every macOS. When it finally goes, this function is the whole port: the policy
// above is expressed in paths rather than in profile syntax, so the compiler
// changes and nothing else does.
func defaultExecutor() (string, string) {
	return "/usr/bin/sandbox-exec", ""
}
