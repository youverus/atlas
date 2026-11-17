package httpapi

import (
	"net/http"
	"strconv"
)

// page is the whole frontend of the skeleton: one document, no build step, no
// framework. It stays inline on purpose. A directory of assets would need
// either an embed of built output — which makes the kernel's build depend on
// node — or a second server; a single document proves the stream works and
// leaves the real frontend free to be anything that speaks the same three
// routes.
//
// The script is deliberately plain: EventSource for the stream, fetch for the
// POST. That is the entire client contract, and writing it here is what makes
// the contract checkable by reading one file.
const pageHTML = `<!doctype html>
<html lang="zh">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>atlas</title>
<style>
  :root { color-scheme: light dark; }
  * { box-sizing: border-box; }
  body {
    margin: 0; height: 100vh; display: flex; flex-direction: column;
    font: 14px/1.55 ui-monospace, SFMono-Regular, Menlo, monospace;
    background: #ffffff; color: #1a1a1a;
  }
  header {
    padding: 10px 16px; border-bottom: 1px solid #e4e4e4;
    display: flex; gap: 16px; align-items: baseline; flex-wrap: wrap;
  }
  header b { font-weight: 600; letter-spacing: .08em; }
  header span { color: #6b6b6b; font-size: 12px; }
  #live { margin-left: auto; font-size: 12px; color: #6b6b6b; }
  #live.on { color: #1a7f37; }
  #transcript {
    flex: 1; overflow-y: auto; padding: 16px; white-space: pre-wrap;
    word-break: break-word;
  }
  .turn { margin: 0 0 18px; }
  .prompt { color: #0a58ca; }
  .prompt::before { content: "> "; color: #9a9a9a; }
  .answer { border-left: 2px solid #d0d0d0; padding-left: 10px; margin-top: 8px; }
  .note { color: #6b6b6b; font-size: 12px; }
  .tool { color: #6b6b6b; font-size: 12px; }
  .error { color: #b3211f; }
  form {
    display: flex; gap: 8px; padding: 12px 16px;
    border-top: 1px solid #e4e4e4; background: #fafafa;
  }
  textarea {
    flex: 1; resize: none; height: 44px; padding: 10px 12px; font: inherit;
    border: 1px solid #d0d0d0; border-radius: 8px; background: #fff; color: inherit;
  }
  button {
    padding: 0 18px; font: inherit; border-radius: 8px;
    border: 1px solid #d0d0d0; background: #fff; color: inherit; cursor: pointer;
  }
  button:disabled { opacity: .45; cursor: default; }
  @media (prefers-color-scheme: dark) {
    body { background: #141414; color: #e6e6e6; }
    header, form { border-color: #2c2c2c; }
    form { background: #1b1b1b; }
    textarea { background: #101010; border-color: #333; }
    button { background: #1b1b1b; border-color: #333; }
    .prompt { color: #7aa2f7; }
    .answer { border-color: #333; }
  }
</style>
</head>
<body>
<header>
  <b>ATLAS</b>
  <span id="facts"></span>
  <span id="live">connecting</span>
</header>
<div id="transcript"></div>
<form id="ask">
  <textarea id="prompt" placeholder="Ask, or Ctrl+Enter to send" autofocus></textarea>
  <button id="send" type="submit">Send</button>
</form>
<script>
(() => {
  const transcript = document.getElementById("transcript");
  const facts = document.getElementById("facts");
  const live = document.getElementById("live");
  const form = document.getElementById("ask");
  const prompt = document.getElementById("prompt");
  const send = document.getElementById("send");

  // One growing turn per prompt. The block for the answer is created on
  // text_start, not on text_delta: a delta arriving before its start would
  // otherwise be dropped, and the first paint would lose the opening word.
  let turn = null;
  let answer = null;
  let thinking = null;

  const note = (text, className) => {
    const line = document.createElement("div");
    line.className = "note " + (className || "");
    line.textContent = text;
    transcript.appendChild(line);
    return line;
  };

  const scroll = () => { transcript.scrollTop = transcript.scrollHeight; };

  const begin = (text) => {
    turn = document.createElement("div");
    turn.className = "turn";
    const head = document.createElement("div");
    head.className = "prompt";
    head.textContent = text;
    turn.appendChild(head);
    transcript.appendChild(turn);
    answer = null;
    thinking = null;
    scroll();
  };

  const block = (className) => {
    if (!turn) begin("(no prompt)");
    const node = document.createElement("div");
    node.className = className;
    turn.appendChild(node);
    return node;
  };

  const render = (event) => {
    switch (event.type) {
      case "agent_start":
        break;
      case "message_start":
        if (event.message && event.message.role === "assistant") {
          answer = null;
          thinking = null;
        }
        break;
      case "message_update": {
        const inner = event.assistantMessageEvent || event.assistant_message_event;
        if (!inner) break;
        if (inner.type === "text_start") {
          answer = block("answer");
        } else if (inner.type === "text_delta" && inner.delta) {
          if (!answer) answer = block("answer");
          answer.textContent += inner.delta;
        } else if (inner.type === "thinking_start") {
          thinking = note("thinking…");
        } else if (inner.type === "thinking_delta" && inner.delta) {
          if (!thinking) thinking = note("thinking…");
          thinking.textContent = "thinking… " + inner.delta;
        } else if (inner.type === "thinking_end") {
          thinking = null;
        }
        break;
      }
      case "tool_execution_start": {
        const size = (event.args && JSON.stringify(event.args).length) || 0;
        note("▶ " + event.toolName + " · " + size + " bytes", "tool");
        break;
      }
      case "tool_execution_update": {
        const last = transcript.querySelector(".tool.live");
        const text = (event.partialResult && String(event.partialResult)) || "";
        if (!last) {
          const line = note("· " + text.slice(0, 60), "tool");
          line.classList.add("live");
        } else {
          last.textContent = "· " + text.slice(0, 60);
        }
        break;
      }
      case "tool_execution_end": {
        transcript.querySelectorAll(".tool.live").forEach((n) => {
          n.classList.remove("live");
        });
        note((event.isError ? "✗ " : "✓ ") + event.toolName, event.isError ? "tool error" : "tool");
        break;
      }
      case "compaction_start":
        note("compacting…");
        break;
      case "compaction_end":
        note("compacted");
        break;
      case "error":
        note((event.error && event.error.content) || "error", "error");
        break;
      case "agent_end":
        note("done");
        break;
    }
    scroll();
  };

  const stream = new EventSource("/api/events");
  stream.addEventListener("hello", (message) => {
    const data = JSON.parse(message.data);
    facts.textContent = [data.version, data.directory, data.model].filter(Boolean).join(" · ");
  });
  stream.addEventListener("agent", (message) => {
    render(JSON.parse(message.data));
  });
  stream.addEventListener("status", (message) => {
    const data = JSON.parse(message.data);
    send.disabled = data.running;
    if (data.error) note(data.error, "error");
    if (!data.running) note("— ready");
  });
  stream.onopen = () => { live.textContent = "connected"; live.classList.add("on"); };
  stream.onerror = () => { live.textContent = "reconnecting"; live.classList.remove("on"); };

  const submit = async () => {
    const text = prompt.value.trim();
    if (!text) return;
    begin(text);
    prompt.value = "";
    send.disabled = true;
    const response = await fetch("/api/ask", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ prompt: text }),
    });
    if (!response.ok) {
      note("start failed: " + (await response.text()).trim(), "error");
      send.disabled = false;
    }
  };

  form.addEventListener("submit", (event) => {
    event.preventDefault();
    submit();
  });
  prompt.addEventListener("keydown", (event) => {
    if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
      event.preventDefault();
      submit();
    }
  });
})();
</script>
</body>
</html>
`

// servePage hands over the document. The page is a constant, so its length is
// known without buffering it.
func (s *Server) servePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(pageHTML)))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(pageHTML))
}
