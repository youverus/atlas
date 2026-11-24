// One EventSource for the whole window.
//
// The server is the only writer: a reload loses nothing but the socket, because
// /api/state says what was already true. That is why this hook owns no
// reconnection logic of its own — EventSource retries, and a status frame fixes
// whatever the gap made stale.

import { useEffect, useRef } from "react";
import type { AgentEvent, PendingQuestion, SideStatus, Status } from "../api";

export interface StreamHandlers {
  onAgent: (event: AgentEvent) => void;
  onStatus: (status: Status) => void;
  onApproval: (question: PendingQuestion) => void;
  onApprovalResolved: (id: string) => void;
  onHello: (facts: { version?: string; directory?: string; model?: string; session?: string }) => void;
  onConnect: (connected: boolean) => void;
  /** 侧对话的事件走另外两个名字：一段流里有两个 agent，靠名字分开比在每一帧
   *  里塞一个标记便宜——折叠处不用为每个 case 多判断一次。 */
  onSideAgent: (event: AgentEvent) => void;
  onSideStatus: (status: SideStatus) => void;
}

export function useStream(handlers: StreamHandlers): void {
  // A ref keeps the subscription stable: the stream must not be torn down and
  // rebuilt every time a handler closure changes identity, which happens on
  // every streamed token.
  const latest = useRef(handlers);
  latest.current = handlers;

  useEffect(() => {
    const source = new EventSource("/api/events");

    source.addEventListener("hello", (message) => {
      latest.current.onHello(JSON.parse((message as MessageEvent).data));
    });
    source.addEventListener("agent", (message) => {
      latest.current.onAgent(JSON.parse((message as MessageEvent).data));
    });
    source.addEventListener("status", (message) => {
      latest.current.onStatus(JSON.parse((message as MessageEvent).data));
    });
    source.addEventListener("approval", (message) => {
      latest.current.onApproval(JSON.parse((message as MessageEvent).data));
    });
    source.addEventListener("approval_resolved", (message) => {
      latest.current.onApprovalResolved(JSON.parse((message as MessageEvent).data).id);
    });
    // 侧对话的两个名字。分开的理由在 StreamHandlers 上：两条流同时活着。
    source.addEventListener("sideagent", (message) => {
      latest.current.onSideAgent(JSON.parse((message as MessageEvent).data));
    });
    source.addEventListener("sidestatus", (message) => {
      latest.current.onSideStatus(JSON.parse((message as MessageEvent).data));
    });

    source.onopen = () => latest.current.onConnect(true);
    source.onerror = () => latest.current.onConnect(false);

    return () => source.close();
  }, []);
}
