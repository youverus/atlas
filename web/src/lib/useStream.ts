// One EventSource for the whole window.
//
// The server is the only writer: a reload loses nothing but the socket, because
// /api/state says what was already true. That is why this hook owns no
// reconnection logic of its own — EventSource retries, and a status frame fixes
// whatever the gap made stale.

import { useEffect, useRef } from "react";
import type { AgentEvent, PendingQuestion, Status } from "../api";

export interface StreamHandlers {
  onAgent: (event: AgentEvent) => void;
  onStatus: (status: Status) => void;
  onApproval: (question: PendingQuestion) => void;
  onHello: (facts: { version?: string; directory?: string; model?: string }) => void;
  onConnect: (connected: boolean) => void;
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

    source.onopen = () => latest.current.onConnect(true);
    source.onerror = () => latest.current.onConnect(false);

    return () => source.close();
  }, []);
}
