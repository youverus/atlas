package channel

import "testing"

func TestParseCommand(t *testing.T) {
	cases := []struct {
		in   string
		name string
		arg  string
		is   bool
	}{
		{"/new", "new", "", true},
		{"  /new  ", "new", "", true},
		{"/cwd /Users/x/work", "cwd", "/Users/x/work", true},
		{"/CWD /x", "cwd", "/x", true},
		{"/stop", "stop", "", true},
		{"/", "", "", false},
		{"hello", "", "", false},
		{"带 /slash 的一句话", "", "", false},
		{"/with-args one two", "with-args", "one two", true},
	}
	for _, c := range cases {
		cmd, ok := parseCommand(c.in)
		if ok != c.is {
			t.Fatalf("parseCommand(%q) is=%v, want %v", c.in, ok, c.is)
		}
		if ok && (cmd.name != c.name || cmd.arg != c.arg) {
			t.Fatalf("parseCommand(%q) = %q/%q, want %q/%q", c.in, cmd.name, cmd.arg, c.name, c.arg)
		}
	}
}

func TestParseApprovalAnswer(t *testing.T) {
	cases := []struct {
		in      string
		allowed bool
		always  bool
		ok      bool
	}{
		{"y", true, false, true},
		{"Y", true, false, true},
		{" a ", true, true, true},
		{"n", false, false, true},
		{"no", false, false, true},
		{"yes", true, false, true},
		{"never", false, false, false},
		{"y 请", false, false, false},
	}
	for _, c := range cases {
		ans, ok := parseApprovalAnswer(c.in)
		if ok != c.ok {
			t.Fatalf("parseApprovalAnswer(%q) ok=%v", c.in, ok)
		}
		if ok && (ans.Allowed != c.allowed || ans.Always != c.always) {
			t.Fatalf("parseApprovalAnswer(%q) = %+v", c.in, ans)
		}
	}
}
