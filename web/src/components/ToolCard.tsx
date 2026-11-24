import { useState } from "react";
import { subagentFailed, subagentPending, type Block } from "../lib/fold";

const SUBAGENT_STATUS: Record<string, string> = {
  queued: "排队中",
  running: "运行中",
  stopping: "停止中",
  succeeded: "已完成",
  failed: "失败",
  canceled: "已取消",
  lost: "运行中断",
};

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
  const task = block.subagent;
  const duration = task?.durationMs !== undefined ? `${(task.durationMs / 1000).toFixed(1)}s` : "";
  const usage = task?.usage ? `${task.usage.totalTokens.toLocaleString()} tokens` : "";
  const metadata = task ? [
    task.id,
    [task.provider, task.model].filter(Boolean).join("/"),
    usage,
  ].filter(Boolean).join(" · ") : "";
  const sections = [
    { label: "参数", text: block.args ?? "" },
    // While the call runs, the last report it made stands in for the result.
    { label: task ? "启动回执" : "结果", text: block.result || block.progress || "" },
    { label: "子代理任务", text: metadata },
    { label: "子代理报告", text: task?.report ?? "" },
    { label: "执行错误", text: task?.error ?? "" },
  ].filter((section) => section.text.length > 0);
  const hasBody = sections.length > 0;
  const failed = block.error || (task && subagentFailed(task));
  const pending = task ? subagentPending(task) : block.open;
  const note = task ? [task.role, task.description].filter(Boolean).join(" · ") || block.note : block.note;
  const status = task ? SUBAGENT_STATUS[task.state] ?? task.state
    : block.open ? "运行中"
    : failed ? "失败"
    : hasBody ? (open ? "收起" : "详情") : "";

  return (
    <div className="tool" data-error={failed ? "true" : "false"}>
      <button
        type="button"
        className="tool__head"
        onClick={() => hasBody && setOpen((value) => !value)}
        aria-expanded={hasBody ? open : undefined}
      >
        <span className={pending ? "spinner" : "dot"} aria-hidden="true" />
        <span className="tool__name">{block.name ?? "tool"}</span>
        {note ? <span className="tool__note" title={task?.description}>{note}</span> : null}
        <span className="tool__status">
          <span aria-live={task ? "polite" : undefined}>{status}</span>
          {duration ? ` · ${duration}` : null}
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
