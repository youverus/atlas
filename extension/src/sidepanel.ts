// 侧边面板：与扩展自己的 Atlas 会话对话。消息经 background 转发，事件从
// 广播帧按会话键过滤；面板本地另存一份纯文本历史，因为 /api/sessions/transcript
// 只读"当前视图会话"，扩展会话切到后台后读不回来。

import { sendRequest, isBroadcast } from "./shared/protocol";
import type { AgentEvent, AssistantEvent, PageContext, StatusEvent } from "./shared/api";
import { appendLog, readLog } from "./shared/log";
import { renderMarkdown } from "./shared/render";
import { loadSettings, saveSettings } from "./shared/settings";

type Item =
  | { id: string; role: "user"; text: string }
  | { id: string; role: "assistant"; text: string; done: boolean }
  | { id: string; role: "tool"; callId: string; name: string; state: "running" | "done" | "error" }
  | { id: string; role: "receipt"; text: string }
  | { id: string; role: "error"; text: string };

// 会话槽关闭（桌面端重启）时的自动恢复只重试一次，失败就停在错误上，
// 避免一个坏会话键把事件流变成重试风暴。
let sessionId = "";
let items: Item[] = [];
let running = false;
let pageContext: PageContext | null = null;
let pendingPrompt: string | null = null;
let recovering = false;
let currentAssistantId: string | null = null;

const dot = document.getElementById("dot") as HTMLSpanElement;
const modelEl = document.getElementById("model") as HTMLSpanElement;
const messagesEl = document.getElementById("messages") as HTMLElement;
const approvalsEl = document.getElementById("approvals") as HTMLElement;
const pageChip = document.getElementById("pageChip") as HTMLElement;
const pageChipTitle = document.getElementById("pageChipTitle") as HTMLElement;
const noticeEl = document.getElementById("notice") as HTMLElement;
const input = document.getElementById("input") as HTMLTextAreaElement;
const sendButton = document.getElementById("send") as HTMLButtonElement;

let seq = 0;
function nextId(): string {
  seq += 1;
  return `i${seq}`;
}

// 历史分工：面板只写自己的用户条目；assistant 文本由 background 从事件流
// 落盘（它看得到每一次流），重开面板时读回。工具卡片与回执是过程信息，
// 不进日志。
async function persistUserItem(item: Item): Promise<void> {
  if (sessionId && item.role === "user") {
    await appendLog(sessionId, { id: item.id, role: "user", text: item.text });
  }
}

async function loadLog(): Promise<void> {
  if (!sessionId) {
    items = [];
    return;
  }
  const list = await readLog(sessionId);
  items = list.map((entry) => ({ ...entry }) as Item);
}

function clearNotice(): void {
  noticeEl.textContent = "";
}

function showNotice(text: string): void {
  noticeEl.textContent = text;
}

// ---------- 渲染 ----------

const itemElements = new Map<string, HTMLElement>();
let renderQueued = false;

function scheduleRender(): void {
  if (renderQueued) return;
  renderQueued = true;
  requestAnimationFrame(() => {
    renderQueued = false;
    renderItems();
  });
}

function renderItems(): void {
  messagesEl.replaceChildren();
  itemElements.clear();
  for (const item of items) {
    const element = renderItem(item);
    itemElements.set(item.id, element);
    messagesEl.append(element);
  }
  messagesEl.scrollTop = messagesEl.scrollHeight;
}

function renderItem(item: Item): HTMLElement {
  const element = document.createElement("div");
  element.className = `msg msg--${item.role}`;
  if (item.role === "user") {
    element.textContent = item.text;
  } else if (item.role === "assistant") {
    element.innerHTML = renderMarkdown(item.text || "…");
    if (!item.done) element.classList.add("msg--running");
  } else if (item.role === "tool") {
    element.dataset.state = item.state;
    element.textContent =
      item.state === "running"
        ? `调用 ${item.name}…`
        : item.state === "error"
          ? `${item.name} 失败`
          : `${item.name} 完成`;
  } else if (item.role === "receipt") {
    element.textContent = item.text;
  } else {
    element.textContent = item.text;
  }
  return element;
}

// 流式 delta 高频到达：只更新对应节点，不整表重建。
function updateAssistantElement(id: string): void {
  const item = items.find((candidate) => candidate.id === id);
  const element = itemElements.get(id);
  if (!item || item.role !== "assistant" || !element) return;
  element.innerHTML = renderMarkdown(item.text);
  messagesEl.scrollTop = messagesEl.scrollHeight;
}

// ---------- 事件处理 ----------

chrome.runtime.onMessage.addListener((message: unknown) => {
  if (!isBroadcast(message)) return;
  if (message.kind === "atlas-connection") {
    dot.dataset.state = message.state;
    dot.title = message.detail ?? message.state;
    return;
  }
  if (message.kind !== "atlas-event") return;
  try {
    if (message.name === "agent") {
      const event = JSON.parse(message.data) as AgentEvent;
      if (eventBelongsToSession(event)) handleAgent(event);
    } else if (message.name === "status") {
      const status = JSON.parse(message.data) as StatusEvent;
      if (eventBelongsToSession(status)) handleStatus(status);
    } else if (message.name === "approval") {
      const approval = JSON.parse(message.data) as { id: string; session?: string; subject?: string; tool?: string };
      if (eventBelongsToSession(approval)) addApproval(approval);
    }
  } catch {
    // 事件帧不是 JSON 时丢弃：数据面保证 JSON，这一支只为防御。
  }
});

function eventBelongsToSession(event: { session?: string }): boolean {
  return event.session !== undefined && event.session === sessionId;
}

function ensureAssistant(): string {
  if (currentAssistantId) return currentAssistantId;
  const item: Item = { id: nextId(), role: "assistant", text: "", done: false };
  items.push(item);
  currentAssistantId = item.id;
  scheduleRender();
  return item.id;
}

function handleAgent(event: AgentEvent): void {
  const stream = event.assistantMessageEvent;
  if (event.type === "message_update" && stream) {
    handleStreamEvent(stream);
    return;
  }
  if (event.type === "message_end") {
    if (currentAssistantId) {
      const item = items.find((candidate) => candidate.id === currentAssistantId);
      if (item && item.role === "assistant") item.done = true;
      currentAssistantId = null;
      scheduleRender();
    }
    return;
  }
  if (event.type === "tool_execution_start") {
    const item: Item = {
      id: nextId(),
      role: "tool",
      callId: event.toolCallId ?? "",
      name: event.toolName ?? "工具",
      state: "running",
    };
    items.push(item);
    scheduleRender();
    return;
  }
  if (event.type === "tool_execution_end") {
    const item = items.find(
      (candidate) =>
        candidate.role === "tool" && candidate.callId !== "" && candidate.callId === event.toolCallId,
    );
    if (item && item.role === "tool") {
      item.state = event.isError ? "error" : "done";
      scheduleRender();
    }
  }
}

function handleStreamEvent(stream: AssistantEvent): void {
  if (stream.type === "text_delta") {
    const id = ensureAssistant();
    const item = items.find((candidate) => candidate.id === id);
    if (item && item.role === "assistant") {
      item.text += stream.delta ?? "";
      updateAssistantElement(id);
    }
    return;
  }
  if (stream.type === "text_end" && currentAssistantId) {
    const item = items.find((candidate) => candidate.id === currentAssistantId);
    if (item && item.role === "assistant" && item.text.trim() === "") {
      // 一个没有正文的 text 块结束时，占位的 "…" 不要留着。
      item.done = true;
      currentAssistantId = null;
      scheduleRender();
    }
  }
  // thinking_* 事件不渲染：面板以正文为主，思考过程留在桌面端轨迹里。
}

function handleStatus(status: StatusEvent): void {
  running = status.running;
  sendButton.disabled = running;
  sendButton.textContent = running ? "运行中" : "发送";
  if (status.running) {
    clearNotice();
    return;
  }
  pendingPrompt = null;
  if (status.error) {
    addError(status.error);
    void recoverSession(status.error);
    return;
  }
  if (status.receipt && status.receipt.length > 0) {
    items.push({ id: nextId(), role: "receipt", text: status.receipt.join(" · ") });
    scheduleRender();
  }
}

function addError(text: string): void {
  items.push({ id: nextId(), role: "error", text });
  scheduleRender();
}

async function recoverSession(error: string): Promise<void> {
  if (recovering || sessionId === "" || !error.includes("session is not open")) return;
  recovering = true;
  try {
    await sendRequest({ type: "open-session", path: sessionId });
    if (pendingPrompt) {
      await sendRequest({ type: "ask", prompt: pendingPrompt, session: sessionId });
    }
  } catch (cause) {
    showNotice(cause instanceof Error ? cause.message : String(cause));
  } finally {
    recovering = false;
  }
}

// ---------- 审批 ----------

function addApproval(approval: { id: string; subject?: string; tool?: string }): void {
  if (approvalsEl.querySelector(`[data-id="${CSS.escape(approval.id)}"]`)) return;
  const card = document.createElement("div");
  card.className = "approval";
  card.dataset.id = approval.id;
  card.textContent = `请求执行 ${approval.tool ?? approval.subject ?? "工具"}`;
  const row = document.createElement("div");
  row.className = "approval__row";
  for (const [act, label] of [["allow", "允许"], ["always", "总是允许"], ["deny", "拒绝"]] as const) {
    const button = document.createElement("button");
    button.type = "button";
    button.textContent = label;
    button.dataset.act = act;
    button.addEventListener("click", () => {
      void sendRequest({
        type: "approve",
        id: approval.id,
        allowed: act !== "deny",
        always: act === "always",
      })
        .then(() => card.remove())
        .catch((cause: unknown) => showNotice(cause instanceof Error ? cause.message : String(cause)));
    });
    row.append(button);
  }
  card.append(row);
  approvalsEl.append(card);
}

// ---------- 动作 ----------

async function ask(): Promise<void> {
  const question = input.value.trim();
  if (!question || running) return;
  clearNotice();
  try {
    if (!sessionId) {
      const key = (await sendRequest({ type: "new-session" })) as string;
      if (!key) throw new Error("创建会话失败");
      sessionId = key;
      await saveSettings({ sessionId });
      items = [];
    }
    const prompt = composePrompt(question);
    const userItem: Item = { id: nextId(), role: "user", text: prompt };
    items.push(userItem);
    input.value = "";
    setPageContext(null);
    pendingPrompt = prompt;
    scheduleRender();
    void persistUserItem(userItem);
    await sendRequest({ type: "ask", prompt, session: sessionId });
  } catch (cause) {
    pendingPrompt = null;
    showNotice(cause instanceof Error ? cause.message : String(cause));
  }
}

// 问本页时把页面内容拼进 prompt 一次发送；面板不维护长上下文，页面事实
// 以消息本身为准，与桌面端看到的一致。
function composePrompt(question: string): string {
  if (!pageContext) return question;
  return [
    "请根据以下网页内容回答问题。",
    "",
    `标题：${pageContext.title}`,
    `地址：${pageContext.url}`,
    "",
    "网页内容：",
    pageContext.text,
    "",
    `问题：${question}`,
  ].join("\n");
}

async function newSession(): Promise<void> {
  try {
    const key = (await sendRequest({ type: "new-session" })) as string;
    if (!key) throw new Error("创建会话失败");
    sessionId = key;
    await saveSettings({ sessionId });
    items = [];
    currentAssistantId = null;
    scheduleRender();
    void refreshState();
  } catch (cause) {
    showNotice(cause instanceof Error ? cause.message : String(cause));
  }
}

async function capturePage(): Promise<void> {
  try {
    const page = (await sendRequest({ type: "get-page" })) as PageContext;
    setPageContext(page);
    clearNotice();
  } catch (cause) {
    showNotice(cause instanceof Error ? `${cause.message}（先在目标页面上点一次 Atlas 图标再试）` : String(cause));
  }
}

function setPageContext(page: PageContext | null): void {
  pageContext = page;
  pageChip.hidden = page === null;
  if (page) pageChipTitle.textContent = page.title || page.url;
}

async function refreshState(): Promise<void> {
  try {
    const state = (await sendRequest({ type: "get-state" })) as { model?: string; session?: string };
    modelEl.textContent = state.model ?? "";
    dot.dataset.state = "connected";
  } catch (cause) {
    dot.dataset.state = "error";
    dot.title = cause instanceof Error ? cause.message : String(cause);
  }
}

sendButton.addEventListener("click", () => void ask());
document.getElementById("newSession")?.addEventListener("click", () => void newSession());
document.getElementById("askPage")?.addEventListener("click", () => void capturePage());
document.getElementById("pageChipRemove")?.addEventListener("click", () => setPageContext(null));
document.getElementById("openOptions")?.addEventListener("click", () => chrome.runtime.openOptionsPage());
input.addEventListener("keydown", (event) => {
  if (event.key === "Enter" && !event.shiftKey) {
    event.preventDefault();
    void ask();
  }
});

void (async () => {
  const settings = await loadSettings();
  sessionId = settings.sessionId;
  await loadLog();
  renderItems();
  void refreshState();
})();
