// background service worker：扩展里唯一持有 token 的地方。所有 REST 调用、
// SSE 事件流、页面抽取都从这里走，再以 runtime 消息广播给面板。
//
// 会话日志的写者有两个：background 写 quick-ask 的用户条目与所有 assistant
// 文本（它看得到每条事件流），面板只写自己输入的用户条目。两边都走
// shared/log 的 append，重开面板时读回。

import { ApiError, AtlasClient, captureActivePage } from "./shared/api";
import { loadSettings, saveSettings, type Settings } from "./shared/settings";
import { appendLog } from "./shared/log";
import { quickPrompt, quickUserLabel, type QuickAction } from "./shared/quick";
import type { AgentEvent, StatusEvent } from "./shared/api";
import type { RequestMessage, ResponseMessage } from "./shared/protocol";

let cached: Settings | null = null;
let client: AtlasClient | null = null;
let streamAbort: AbortController | null = null;
let currentAssistant = "";
let seq = 0;

// streamFailures 记录连续失败次数：断流重连要退避，不能在服务未启动时打满 CPU。
let streamFailures = 0;
let reconnectTimer: ReturnType<typeof setTimeout> | null = null;

const RECONNECT_DELAYS_MS = [500, 1000, 2000, 4000, 8000, 15000];

async function getSettings(): Promise<Settings> {
  if (!cached) cached = await loadSettings();
  return cached;
}

async function getClient(): Promise<AtlasClient> {
  const settings = await getSettings();
  client = new AtlasClient(settings.baseUrl, settings.token);
  return client;
}

function broadcast(message: unknown): void {
  // 面板可能没开：Receiving end does not exist 是常态，不是错误。
  chrome.runtime.sendMessage(message).catch(() => {});
}

function announceConnection(state: "connected" | "offline" | "error", detail?: string): void {
  broadcast({ kind: "atlas-connection", state, detail });
}

// ensureStream 维持一条 /api/events 长流。断开后按退避表重连；被 guard 拒绝
// （未配对且无 token）时不再重试，等选项页配对或保存设置后重新拉起。
async function ensureStream(): Promise<void> {
  if (streamAbort) return;
  const settings = await getSettings();
  const atlas = await getClient();
  const abort = new AbortController();
  streamAbort = abort;
  try {
    await atlas.events((name, data) => {
      streamFailures = 0;
      if (name === "agent") {
        const event = JSON.parse(data) as AgentEvent;
        if (event.session === settings.sessionId) handleAgentEvent(event);
        broadcast({ kind: "atlas-event", name, data });
      } else if (name === "status") {
        const status = JSON.parse(data) as StatusEvent;
        if (status.session === settings.sessionId && !status.running) flushAssistant(settings.sessionId);
        broadcast({ kind: "atlas-event", name, data });
      } else if (name === "approval") {
        broadcast({ kind: "atlas-event", name, data });
      }
    }, abort.signal);
    announceConnection("offline");
  } catch (cause) {
    if (abort.signal.aborted) return;
    const status = cause instanceof ApiError ? cause.status : undefined;
    if (status === 401 || status === 403) {
      announceConnection("error", "连接被拒绝：请先在选项页完成一键配对，或检查 token");
      return;
    }
    announceConnection("error", String(cause));
  } finally {
    streamAbort = null;
  }
  scheduleReconnect();
}

// assistant 文本积累：text_delta 只属于流中的当前消息，message_end 时落盘。
// 失败的 turn 在 status 收尾处丢弃半截文本，避免日志里出现没头没尾的回答。
function handleAgentEvent(event: AgentEvent): void {
  const stream = event.assistantMessageEvent;
  if (event.type === "message_update" && stream?.type === "text_delta") {
    currentAssistant += stream.delta ?? "";
    return;
  }
  if (event.type === "message_end") flushAssistant(event.session ?? "");
}

async function flushAssistant(sessionId: string): Promise<void> {
  const text = currentAssistant;
  currentAssistant = "";
  if (!sessionId || !text.trim()) return;
  seq += 1;
  await appendLog(sessionId, { id: `b${seq}`, role: "assistant", text });
}

function scheduleReconnect(): void {
  if (reconnectTimer) clearTimeout(reconnectTimer);
  const delay = RECONNECT_DELAYS_MS[Math.min(streamFailures, RECONNECT_DELAYS_MS.length - 1)];
  streamFailures += 1;
  reconnectTimer = setTimeout(() => void ensureStream(), delay);
}

function restartStream(): void {
  streamFailures = 0;
  if (reconnectTimer) clearTimeout(reconnectTimer);
  streamAbort?.abort();
  streamAbort = null;
  void ensureStream();
}

// createSession 开一个新会话，cwd 固定为内核启动目录（桌面端正常启动即
// home）：会话文件落在默认目录，CLI 在同一目录跑 atlas 用 /resume 就能
// 打开扩展创建的会话。首次会把桌面端当前视图切到新会话，这是
// /api/sessions/new 的既有语义。
async function createSession(): Promise<string> {
  const atlas = await getClient();
  const before = await atlas.state();
  await atlas.newSession(before.directory || undefined);
  const after = await atlas.state();
  return after.session ?? "";
}

// ensureSession 返回扩展自己的会话键；没有就按上面的规则开一个。
async function ensureSession(): Promise<string> {
  const settings = await getSettings();
  if (settings.sessionId) return settings.sessionId;
  const sessionId = await createSession();
  await saveSettings({ sessionId });
  if (cached) cached.sessionId = sessionId;
  return sessionId;
}

// quickAsk 是右键菜单与划词条的共同入口：确保会话、记日志、发问、开面板。
async function quickAsk(action: QuickAction, text: string, tabId?: number): Promise<void> {
  const atlas = await getClient();
  const sessionId = await ensureSession();
  seq += 1;
  await appendLog(sessionId, { id: `q${seq}`, role: "user", text: quickUserLabel(action, text) });
  await atlas.ask(quickPrompt(action, text), sessionId);
  if (tabId) {
    // 划词/菜单点击是用户手势，面板应能打开；个别版本手势不透传时静默
    // 失败——提问照常执行，用户点工具栏图标就能看到正在运行的面板。
    chrome.sidePanel.open({ tabId }).catch(() => {});
  }
}

const SELECTION_ACTIONS: Record<string, QuickAction> = {
  "atlas-explain": "explain",
  "atlas-translate": "translate",
  "atlas-summarize": "summarize",
};

chrome.runtime.onInstalled.addListener(() => {
  chrome.contextMenus.create({ id: "atlas-explain", title: "用 Atlas 解释所选文字", contexts: ["selection"] });
  chrome.contextMenus.create({ id: "atlas-translate", title: "用 Atlas 翻译所选文字", contexts: ["selection"] });
  chrome.contextMenus.create({ id: "atlas-summarize", title: "用 Atlas 总结所选文字", contexts: ["selection"] });
  chrome.contextMenus.create({ id: "atlas-summarize-page", title: "用 Atlas 总结本页", contexts: ["page"] });
});

chrome.contextMenus.onClicked.addListener((info, tab) => {
  void (async () => {
    try {
      if (info.menuItemId === "atlas-summarize-page") {
        // 右键菜单点击已授予该标签页 activeTab 权限，抽取可用。
        const page = await captureActivePage();
        await quickAsk("summarize_page", `标题：${page.title}\n地址：${page.url}\n\n${page.text}`, tab?.id);
        return;
      }
      const action = SELECTION_ACTIONS[String(info.menuItemId)];
      const text = (info.selectionText ?? "").trim();
      if (action && text) await quickAsk(action, text, tab?.id);
    } catch (cause) {
      announceConnection("error", cause instanceof Error ? cause.message : String(cause));
    }
  })();
});

// handlers：每种请求一个小函数，签名统一为 (message) => data。
const handlers: Record<string, (message: RequestMessage) => Promise<unknown>> = {
  async "test-connection"(message) {
    const baseUrl = typeof message.baseUrl === "string" ? message.baseUrl : "";
    const token = typeof message.token === "string" ? message.token : "";
    const atlas = new AtlasClient(baseUrl, token);
    return atlas.connection();
  },
  async "get-state"() {
    const atlas = await getClient();
    return atlas.state();
  },
  async ask(message) {
    const atlas = await getClient();
    const prompt = String(message.prompt ?? "");
    const session = await ensureSession();
    await atlas.ask(prompt, session);
    return null;
  },
  async "new-session"() {
    const sessionId = await createSession();
    await saveSettings({ sessionId });
    if (cached) cached.sessionId = sessionId;
    return sessionId;
  },
  async "pair"(message) {
    const settings = await getSettings();
    const baseUrl =
      typeof message.baseUrl === "string" && message.baseUrl.trim() !== ""
        ? message.baseUrl.trim().replace(/\/+$/, "")
        : settings.baseUrl;
    // 配对请求不带 token：guard 对未获准扩展只放行这一个入口，桌面端窗口
    // 上的审批卡才是真正的落子处。
    await new AtlasClient(baseUrl, "").pair();
    await saveSettings({ baseUrl });
    cached = null;
    restartStream();
    return null;
  },
  async approve(message) {
    const atlas = await getClient();
    await atlas.approve(String(message.id ?? ""), message.allowed === true, message.always === true);
    return null;
  },
  async "get-page"() {
    return captureActivePage();
  },
  async "quick-ask"(message) {
    const action = String(message.action ?? "") as QuickAction;
    const text = String(message.text ?? "").trim();
    // 划词条与右键菜单只发白名单动作，这里再核一次。
    if (!text || !(["explain", "translate", "summarize", "summarize_page"] as string[]).includes(action)) {
      throw new Error(`unknown quick action: ${action}`);
    }
    await quickAsk(action, text);
    return null;
  },
  async "save-settings"(message) {
    const patch: Record<string, string> = {};
    if (typeof message.baseUrl === "string" && message.baseUrl.trim() !== "") {
      patch.baseUrl = message.baseUrl;
    }
    if (typeof message.token === "string") {
      patch.token = message.token.trim();
    }
    await saveSettings(patch);
    cached = null;
    restartStream();
    return null;
  },
};

chrome.runtime.onMessage.addListener((message: RequestMessage, _sender, sendResponse) => {
  const handler = handlers[message?.type];
  if (!handler) return false;
  void handler(message)
    .then((data): ResponseMessage => ({ ok: true, data }))
    .catch((cause: unknown): ResponseMessage => ({
      ok: false,
      error: cause instanceof Error ? cause.message : String(cause),
    }))
    .then(sendResponse);
  return true; // 异步 sendResponse
});

// 点工具栏图标即开侧边面板。
chrome.sidePanel
  .setPanelBehavior({ openPanelOnActionClick: true })
  .catch(() => {});

void ensureStream();
