// 扩展选项页：一键配对（桌面端审批卡落子）或手动 token 两条连接路径，
// 配对成功后当场测一次 /api/state 确认连通。

import { normalizeBaseUrl } from "./shared/settings";
import { sendRequest } from "./shared/protocol";

const baseUrlInput = document.getElementById("baseUrl") as HTMLInputElement;
const tokenInput = document.getElementById("token") as HTMLInputElement;
const statusEl = document.getElementById("status") as HTMLSpanElement;
const pairStatusEl = document.getElementById("pairStatus") as HTMLSpanElement;
const saveButton = document.getElementById("save") as HTMLButtonElement;
const pairButton = document.getElementById("pair") as HTMLButtonElement;

chrome.storage.local.get(["baseUrl", "token"]).then((stored) => {
  baseUrlInput.value = typeof stored["baseUrl"] === "string" ? stored["baseUrl"] : "";
  if (typeof stored["token"] === "string") tokenInput.value = stored["token"];
});

pairButton.addEventListener("click", () => {
  const baseUrl = normalizeBaseUrl(baseUrlInput.value);
  pairButton.disabled = true;
  pairShow("等待桌面端确认…", true);
  void sendRequest({ type: "pair", baseUrl })
    .then(() => sendRequest({ type: "get-state" }))
    .then(() => pairShow(`已配对并连接 ${baseUrl}`, true))
    .catch((cause: unknown) => {
      const message = cause instanceof Error ? cause.message : String(cause);
      if (message.includes("not configured")) {
        pairShow("该实例不支持配对（如 atlas -serve），请粘贴 token", false);
      } else if (message.includes("denied")) {
        pairShow("桌面端拒绝了请求；可重试后在其窗口点「允许」，或改用 token", false);
      } else {
        pairShow(`连不上桌面端：${message}`, false);
      }
    })
    .finally(() => {
      pairButton.disabled = false;
    });
});

saveButton.addEventListener("click", () => {
  const baseUrl = normalizeBaseUrl(baseUrlInput.value);
  const token = tokenInput.value.trim();
  if (!token) {
    show("请先粘贴 token", false);
    return;
  }
  saveButton.disabled = true;
  show("正在连接…", true);
  void sendRequest({ type: "save-settings", baseUrl, token })
    .then(() => sendRequest({ type: "test-connection", baseUrl, token }))
    .then(() => show(`已连接 ${baseUrl}`, true))
    .catch((cause: unknown) => {
      show(cause instanceof Error ? cause.message : String(cause), false);
    })
    .finally(() => {
      saveButton.disabled = false;
    });
});

function show(text: string, ok: boolean): void {
  statusEl.textContent = text;
  statusEl.className = `status ${ok ? "ok" : "err"}`;
}

function pairShow(text: string, ok: boolean): void {
  pairStatusEl.textContent = text;
  pairStatusEl.className = `status ${ok ? "ok" : "err"}`;
}
