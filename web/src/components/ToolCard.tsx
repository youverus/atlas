import { useState } from "react";
import type { Block } from "../lib/fold";

// A tool call is one card: the head is the claim, the body is the evidence.
// Collapsed by default because a turn with eight calls otherwise buries the
// answer under its own working.
export function ToolCard({ block }: { block: Block }) {
  const [open, setOpen] = useState(false);
  const body = block.detail || block.progress || "";
  const hasBody = body.length > 0;

  return (
    <div className="tool" data-error={block.error ? "true" : "false"}>
      <button
        type="button"
        className="tool__head"
        onClick={() => hasBody && setOpen((value) => !value)}
        aria-expanded={hasBody ? open : undefined}
      >
        <span className={block.open ? "spinner" : "dot"} aria-hidden="true" />
        <span className="tool__name">{block.name ?? "tool"}</span>
        {block.note ? <span className="tool__note">{block.note}</span> : null}
        <span className="tool__status">
          {block.open ? "running" : block.error ? "failed" : hasBody ? (open ? "hide" : "detail") : ""}
        </span>
      </button>
      {open && hasBody ? (
        <div className="tool__body">
          <pre>{body}</pre>
        </div>
      ) : null}
    </div>
  );
}
