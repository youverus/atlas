import { useState } from "react";
import type { Block } from "../lib/fold";

// A tool call is one card: the head is the claim, the body is the evidence.
// Collapsed by default because a turn with eight calls otherwise buries the
// answer under its own working.
//
// What the body holds is the whole call — the arguments it was given and what
// came back — because the card is the only place the window can show either.
// The terminal draws a one-line preview and never opens; a reader there
// scrolls, so the preview is the record. Here the preview is a promise that
// something can be opened, and a card that opens onto nothing is a dead button.
export function ToolCard({ block }: { block: Block }) {
  const [open, setOpen] = useState(false);
  const sections = [
    { label: "参数", text: block.args ?? "" },
    // While the call runs, the last report it made stands in for the result.
    { label: "结果", text: block.result || block.progress || "" },
  ].filter((section) => section.text.length > 0);
  const hasBody = sections.length > 0;

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
          {block.open
            ? "运行中"
            : block.error
              ? "失败"
              : hasBody
                ? open
                  ? "收起"
                  : "详情"
                : ""}
        </span>
      </button>
      {open && hasBody ? (
        <div className="tool__body">
          {sections.map((section) => (
            <div className="tool__section" key={section.label}>
              <span className="tool__label">{section.label}</span>
              <pre>{section.text}</pre>
            </div>
          ))}
        </div>
      ) : null}
    </div>
  );
}
