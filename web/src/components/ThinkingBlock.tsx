import { useState } from "react";
import type { Block } from "../lib/fold";

// Reasoning is folded by default. It is the working, not the answer: a reader
// who wants it can open it, and a reader who does not should never have to
// scroll past it to reach the reply.
export function ThinkingBlock({ block, defaultOpen = false }: { block: Block; defaultOpen?: boolean }) {
  const [open, setOpen] = useState(defaultOpen);

  return (
    <div className="thinking">
      <button type="button" className="thinking__head" onClick={() => setOpen((value) => !value)}>
        {block.open ? <span className="spinner" aria-hidden="true" /> : null}
        <span className="thinking__label">{block.open ? "思考中" : "已思考"}</span>
        <span style={{ marginLeft: "auto" }}>{open ? "收起" : "展开"}</span>
      </button>
      {open ? <div className="thinking__body">{block.text}</div> : null}
    </div>
  );
}
