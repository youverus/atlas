package skill

import (
	"strings"
	"testing"
)

// The subset exists because a description with a colon in it is the normal case,
// and a reader that cuts at the first colon produces half a sentence the model
// then chooses by. Every row here is a shape a real SKILL.md takes.
func TestTheFrontmatterSubset(t *testing.T) {
	cases := []struct {
		name  string
		block string
		want  map[string]string
	}{
		{
			"one line",
			"name: release\ndescription: Cut a release.",
			map[string]string{"name": "release", "description": "Cut a release."},
		},
		{
			"a colon in the value",
			"name: release\ndescription: Use this when: the build fails.",
			map[string]string{"name": "release", "description": "Use this when: the build fails."},
		},
		{
			"double quotes",
			`name: "release"`,
			map[string]string{"name": "release"},
		},
		{
			"single quotes",
			`name: 'release'`,
			map[string]string{"name": "release"},
		},
		{
			"a trailing comment",
			"name: release\ndescription: Cut a release. # not part of it",
			map[string]string{"name": "release", "description": "Cut a release."},
		},
		{
			"a hash inside quotes",
			`description: "cut #123"`,
			map[string]string{"description": "cut #123"},
		},
		{
			"a kept block",
			"description: |\n  one\n  two",
			map[string]string{"description": "one\ntwo"},
		},
		{
			"a folded block",
			"description: >\n  one\n  two",
			map[string]string{"description": "one two"},
		},
		{
			"a folded block with two paragraphs",
			"description: >\n  one\n\n  two",
			map[string]string{"description": "one\ntwo"},
		},
		{
			"a folded description with a strip indicator",
			"name: release\ndescription: >-\n  Use this when: a release is ready.\n  Keep going.",
			map[string]string{"name": "release", "description": "Use this when: a release is ready. Keep going."},
		},
		{
			"a folded block with a keep indicator",
			"description: >+\n  one\n  two\n\nname: release",
			map[string]string{"name": "release", "description": "one two"},
		},
		{
			"a literal block with a strip indicator and a comment",
			"description: |- # retain the internal newline\n  one\n  two",
			map[string]string{"description": "one\ntwo"},
		},
		{
			"a literal block with a keep indicator",
			"description: |+\n  one\n  two\n",
			map[string]string{"description": "one\ntwo"},
		},
		{
			"a quoted block indicator is a string",
			`description: ">-"`,
			map[string]string{"description": ">-"},
		},
		{
			"a block with deeper indentation kept",
			"description: |\n  one\n    two",
			map[string]string{"description": "one\n  two"},
		},
		{
			"unknown keys are ignored",
			"license: MIT\nname: release",
			map[string]string{"name": "release"},
		},
		{
			"an unknown key with a nested map",
			"metadata:\n  author: me\n  tags: [a, b]\nname: release",
			map[string]string{"name": "release"},
		},
		{
			"comments and blank lines",
			"# the name\n\nname: release\n",
			map[string]string{"name": "release"},
		},
		{
			"windows line endings",
			"name: release\r\ndescription: Cut a release.\r\n",
			map[string]string{"name": "release", "description": "Cut a release."},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values, err := parseFrontmatter("---\n" + tc.block + "\n---\nbody\n")
			if err != nil {
				t.Fatalf("parseFrontmatter: %v", err)
			}
			if len(values) != len(tc.want) {
				t.Fatalf("values = %v, want %v", values, tc.want)
			}
			for key, want := range tc.want {
				if values[key] != want {
					t.Fatalf("%s = %q, want %q", key, values[key], want)
				}
			}
			// The keys the prompt does not use are not kept either: they would be
			// a second place for a value to go stale.
			for _, unused := range []string{"license", "metadata"} {
				if _, kept := values[unused]; kept {
					t.Fatalf("values = %v, want %s dropped", values, unused)
				}
			}
		})
	}
}

// Everything this reader cannot read is an error with a line number. A guessed
// value would become the description the model picks by, and a wrong description
// makes a skill invisible in a way nobody can debug.
func TestTheFrontmatterSubsetRejects(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
		line string
	}{
		{"an anchor", "---\ndescription: &why one\n---\n", "anchor", "frontmatter line 2"},
		{"an alias", "---\ndescription: *why\n---\n", "alias", "frontmatter line 2"},
		{"a flow sequence", "---\nallowed-tools: [bash, read]\n---\n", "flow sequence", "frontmatter line 2"},
		{"a flow mapping", "---\nname: {a: b}\n---\n", "flow mapping", "frontmatter line 2"},
		{"an indented line with no key", "---\n  name: release\n---\n", "indented line", "frontmatter line 2"},
		{"an indented continuation", "---\nname: release\ndescription: one\n  two\n---\n", "indented block under description:", "frontmatter line 4"},
		{"a key with no value", "---\nname: release\ndescription:\n---\n", "has no value", "frontmatter line 3"},
		{"a line that is not a pair", "---\nrelease\n---\n", "not a key: value pair", "frontmatter line 2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseFrontmatter(tc.text)
			if err == nil {
				t.Fatal("want an error")
			}
			for _, want := range []string{tc.want, tc.line, "this reader reads one-line values"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %q, want it to contain %q", err, want)
				}
			}
		})
	}
}

// A file that does not start with --- is prose, not a broken skill, and the two
// are reported differently: one is a file to ignore, the other a file to fix.
func TestAFileWithoutFrontmatterIsNotASkill(t *testing.T) {
	for _, text := range []string{"", "Just some notes.\n", "\n---\nname: release\n---\n"} {
		if _, err := parseFrontmatter(text); err != errNoFrontmatter {
			t.Fatalf("parseFrontmatter(%.20q) = %v, want errNoFrontmatter", text, err)
		}
	}
}

// The second --- in a file closes the block, and the one after it belongs to the
// body: a skill whose instructions contain a horizontal rule is valid, and
// treating it as a second document would refuse a file that reads fine.
func TestASecondDocumentIsJustTheBody(t *testing.T) {
	values, err := parseFrontmatter("---\nname: release\n---\n\nRelease steps:\n\n---\n\nmore\n")
	if err != nil {
		t.Fatalf("parseFrontmatter: %v", err)
	}
	if values["name"] != "release" {
		t.Fatalf("values = %v, want the name", values)
	}
}
