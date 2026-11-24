package feishu

import "testing"

func TestDowngrade(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain stays", "你好，世界", "你好，世界"},
		{"bold", "**重要**的事", "重要的事"},
		{"dunder", "__重要__的事", "重要的事"},
		{"italic", "*重要*的事", "重要的事"},
		{"inline code protects emphasis", "`code **keep** this`", "code **keep** this"},
		{"link keeps target", "见[文档](https://a.b/c)即可", "见文档（https://a.b/c）即可"},
		{"image", "![截图](https://a.b/p.png)", "截图"},
		{"heading", "## 标题\n正文", "标题\n正文"},
		{"quote", "> 引用一行", "引用一行"},
		{"asterisk bullet", "* 一项\n* 二项", "• 一项\n• 二项"},
		{"dash bullet stays", "- 一项", "- 一项"},
		{"snake case stays", "foo_bar_baz 不动", "foo_bar_baz 不动"},
		{"word boundary underscore", "写成 _中_ 试试", "写成 中 试试"},
		{"fence passes verbatim", "如下：\n```go\nx**y\n```\n完", "如下：\n```go\nx**y\n```\n完"},
		{"unclosed fence passes verbatim", "如下：\n```go\nx**y", "如下：\n```go\nx**y"},
		{"mixed", "**先** 看 `a*b`，再 *后*", "先 看 a*b，再 后"},
	}
	for _, c := range cases {
		if got := Downgrade(c.in); got != c.want {
			t.Errorf("%s: Downgrade(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
