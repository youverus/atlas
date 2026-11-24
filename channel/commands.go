package channel

import (
	"strings"
)

// command is one chat command the gateway handles itself. The set is small on
// purpose: everything else the reader might want — /model, /compact, /fork —
// has a keyboard and a mouse on some other surface; in a chat the value is a
// conversation that starts over and a runaway turn that stops.
type command struct {
	name string
	arg  string
}

// parseCommand splits a leading "/name [argument]" off the text. Anything not
// starting with "/" is a prompt; "/cwd /a/b" has the argument, "/new" does not.
func parseCommand(text string) (command, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "/") {
		return command{}, false
	}
	rest := strings.TrimLeft(trimmed[1:], "")
	if rest == "" {
		return command{}, false
	}
	name, arg, _ := strings.Cut(rest, " ")
	return command{name: strings.ToLower(name), arg: strings.TrimSpace(arg)}, true
}

const helpText = `聊天里能用的命令：
  /new     这个聊天开一个新会话，之前的上下文不再接续
  /stop    中止正在跑的这一轮
  /status  当前会话、模型与工作目录
  /cwd     仅管理员：切换这个聊天的工作目录（并开新会话）
  /help    这张表
其余直接说话就好；群里记得 @ 我。`
