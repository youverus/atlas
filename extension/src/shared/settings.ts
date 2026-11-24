// 连接设置的唯一读写入口。baseUrl 与 token 来自桌面端设置页的
// "浏览器扩展" 卡片；sessionId 是扩展自己开的会话文件路径（会话键）。

export interface Settings {
  baseUrl: string;
  token: string;
  sessionId: string;
}

// 与桌面端 bindServer 的默认端口一致：装好即连，不需要发现流程。
export const DEFAULT_BASE_URL = "http://127.0.0.1:7788";

export async function loadSettings(): Promise<Settings> {
  const stored = await chrome.storage.local.get(["baseUrl", "token", "sessionId"]);
  const baseUrl = normalizeBaseUrl(stored["baseUrl"]);
  return {
    baseUrl,
    token: typeof stored["token"] === "string" ? stored["token"] : "",
    sessionId: typeof stored["sessionId"] === "string" ? stored["sessionId"] : "",
  };
}

export async function saveSettings(patch: Partial<Settings>): Promise<void> {
  await chrome.storage.local.set(patch);
}

// 归一化地址：去空白与结尾斜杠；空值回退到默认端口——大多数单机用户
// 应当零配置连通，地址栏只留给改过端口的人。
export function normalizeBaseUrl(raw: unknown): string {
  if (typeof raw !== "string" || raw.trim() === "") {
    return DEFAULT_BASE_URL;
  }
  return raw.trim().replace(/\/+$/, "");
}
