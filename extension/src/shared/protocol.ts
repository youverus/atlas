// 扩展内消息协议：content / sidepanel / options 都通过 background 转发到
// Atlas 服务。token 只在 background 里出现，不进面板与页面。

import type { AgentEvent } from "./api";

export interface RequestMessage {
  type: string;
  [key: string]: unknown;
}

export interface ResponseMessage {
  ok: boolean;
  data?: unknown;
  error?: string;
}

/** background → panel 的 SSE 广播帧。 */
export interface EventBroadcast {
  kind: "atlas-event";
  name: string;
  data: string;
}

/** 连接状态变化通知，面板据此点亮状态点。 */
export interface ConnectionBroadcast {
  kind: "atlas-connection";
  state: "connected" | "offline" | "error";
  detail?: string;
}

export type Broadcast = EventBroadcast | ConnectionBroadcast;

export function isBroadcast(value: unknown): value is Broadcast {
  if (typeof value !== "object" || value === null) return false;
  const kind = (value as Broadcast).kind;
  return kind === "atlas-event" || kind === "atlas-connection";
}

/** 向 background 发一条请求并拿结果；错误以字符串回传。 */
export function sendRequest(message: RequestMessage): Promise<unknown> {
  return new Promise((resolve, reject) => {
    chrome.runtime.sendMessage(message, (reply: ResponseMessage | undefined) => {
      if (chrome.runtime.lastError) {
        reject(new Error(chrome.runtime.lastError.message));
        return;
      }
      if (!reply) {
        reject(new Error("empty reply"));
        return;
      }
      if (reply.ok) {
        resolve(reply.data);
      } else {
        reject(new Error(reply.error ?? "unknown error"));
      }
    });
  });
}

/** panel 侧按会话过滤事件：多会话构建里 agent/status 事件都带 session 键，
 *  空键事件属于桌面视图的单会话流，不属于扩展自己的会话，忽略。 */
export function eventBelongsTo(event: { session?: string }, sessionId: string): boolean {
  if (sessionId === "") return false;
  return event.session === sessionId;
}
