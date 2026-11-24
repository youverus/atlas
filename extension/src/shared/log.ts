// 共享会话日志：面板与 background 各自追加，重开面板后用于恢复历史。
// 只存 user 与 assistant 的文本；工具卡片与回执是过程信息，不进日志。

export interface LogItem {
  id: string;
  role: "user" | "assistant";
  text: string;
}

const CAP = 100;

function logKey(sessionId: string): string {
  return `log:${sessionId}`;
}

export async function readLog(sessionId: string): Promise<LogItem[]> {
  if (!sessionId) return [];
  const key = logKey(sessionId);
  const stored = await chrome.storage.local.get(key);
  const list = stored[key];
  return Array.isArray(list) ? (list as LogItem[]) : [];
}

export async function appendLog(sessionId: string, item: LogItem): Promise<void> {
  if (!sessionId || !item.text.trim()) return;
  const key = logKey(sessionId);
  const list = await readLog(sessionId);
  list.push(item);
  await chrome.storage.local.set({ [key]: list.slice(-CAP) });
}
