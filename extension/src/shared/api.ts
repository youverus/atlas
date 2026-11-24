// Atlas 数据面客户端：REST + SSE。与 go/web/src/api.ts 是同一份契约的两个
// 读法，这里只保留扩展用到的路由；事件帧解析为手写实现，因为 MV3 的
// service worker 没有 EventSource，只有 fetch 的流式 body。

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message);
  }
}

/** 一条 agent 生命周期事件，镜像 wire.AgentEvent。 */
export interface AgentEvent {
  session?: string;
  type: string;
  message?: { role?: string; content?: unknown };
  assistantMessageEvent?: AssistantEvent;
  toolCallId?: string;
  toolName?: string;
  args?: unknown;
  isError?: boolean;
  errorMessage?: string;
}

/** 一条 provider 流事件，镜像 wire.AssistantEvent。 */
export interface AssistantEvent {
  type: string;
  delta?: string;
  content?: string;
}

/** 事件流里的 status 帧：一次 turn 的括号与回执。 */
export interface StatusEvent {
  session?: string;
  running: boolean;
  error?: string;
  receipt?: string[];
}

export interface State {
  version?: string;
  directory?: string;
  model?: string;
  session?: string;
  running: boolean;
}

export interface ConnectionInfo {
  url: string;
  token?: string;
}

export interface PageContext {
  title: string;
  url: string;
  text: string;
}

// 与桌面端 web UI 一致：超过这个长度的页面正文会被截断，避免 prompt 失控。
const PAGE_TEXT_LIMIT = 24_000;

export class AtlasClient {
  constructor(
    readonly baseUrl: string,
    readonly token: string,
  ) {}

  private headers(extra?: HeadersInit): Headers {
    const headers = new Headers(extra);
    if (this.token) headers.set("Authorization", `Bearer ${this.token}`);
    return headers;
  }

  private async request<T>(path: string, init?: RequestInit): Promise<T> {
    const response = await fetch(this.baseUrl + path, {
      ...init,
      headers: this.headers(init?.headers),
    });
    if (!response.ok) {
      const detail = (await response.text()).trim();
      throw new ApiError(detail || `${response.status} ${response.statusText}`, response.status);
    }
    const body = (await response.text()).trim();
    if (!body) return undefined as T;
    return JSON.parse(body) as T;
  }

  connection(): Promise<ConnectionInfo> {
    return this.request<ConnectionInfo>("/api/connection");
  }

  state(): Promise<State> {
    return this.request<State>("/api/state");
  }

  ask(prompt: string, session?: string): Promise<void> {
    return this.request<void>("/api/ask", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(session ? { prompt, session } : { prompt }),
    });
  }

  newSession(cwd?: string): Promise<void> {
    return this.request<void>("/api/sessions/new", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(cwd ? { cwd } : {}),
    });
  }

  /** 一键配对：请求在桌面端弹审批卡，读者允许后本扩展 Origin 进入白名单。
   *  服务端会阻塞到卡片被回答（或超时拒绝），所以这个 Promise 的返回就是结果。 */
  pair(): Promise<void> {
    return this.request<void>("/api/extension/pair", { method: "POST" });
  }

  openSession(path: string): Promise<void> {
    return this.request<void>("/api/sessions/open", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ path }),
    });
  }

  approve(id: string, allowed: boolean, always: boolean): Promise<void> {
    return this.request<void>("/api/approval", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id, allowed, always }),
    });
  }

  /** 打开事件流并逐帧回调；连接断开或 signal 中止时返回。 */
  async events(onEvent: (name: string, data: string) => void, signal: AbortSignal): Promise<void> {
    const response = await fetch(`${this.baseUrl}/api/events`, {
      headers: this.headers(),
      signal,
    });
    if (!response.ok || !response.body) {
      throw new ApiError(`stream ${response.status}`, response.status);
    }
    const reader = response.body.pipeThrough(new TextDecoderStream()).getReader();
    let buffer = "";
    for (;;) {
      const { done, value } = await reader.read();
      if (done) return;
      buffer += value;
      for (;;) {
        const boundary = buffer.indexOf("\n\n");
        if (boundary < 0) break;
        const frame = buffer.slice(0, boundary);
        buffer = buffer.slice(boundary + 2);
        const parsed = parseFrame(frame);
        if (parsed) onEvent(parsed.name, parsed.data);
      }
    }
  }
}

// 一个 SSE 帧：event/data 行与 ":" 注释行，data 可多行。无名字的帧不投递。
function parseFrame(frame: string): { name: string; data: string } | null {
  let name = "";
  const data: string[] = [];
  for (const line of frame.split("\n")) {
    if (line.startsWith(":")) continue;
    if (line.startsWith("event:")) name = line.slice(6).trim();
    else if (line.startsWith("data:")) data.push(line.slice(5).trimStart());
  }
  if (!name) return null;
  return { name, data: data.join("\n") };
}

/** 在活动标签页抽取页面正文。必须由 background 调用；activeTab 授权后可用。 */
export async function captureActivePage(): Promise<PageContext> {
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  if (!tab?.id) throw new Error("no active tab");
  const results = await chrome.scripting.executeScript({
    target: { tabId: tab.id },
    func: extractPage,
  });
  const page = results?.[0]?.result as PageContext | undefined;
  if (!page) throw new Error("cannot read this page");
  return page;
}

// 注入页面执行的抽取函数：只能是自包含代码，不得引用外部符号。
function extractPage(): PageContext {
  const clone = document.body?.cloneNode(true) as HTMLElement | undefined;
  clone?.querySelectorAll("script, style, noscript, svg").forEach((node) => node.remove());
  let text = (clone?.innerText ?? "").replace(/\n{3,}/g, "\n\n").trim();
  if (text.length > PAGE_TEXT_LIMIT) {
    text = `${text.slice(0, PAGE_TEXT_LIMIT)}\n\n（内容过长，已截断）`;
  }
  return { title: document.title ?? "", url: location.href, text };
}
