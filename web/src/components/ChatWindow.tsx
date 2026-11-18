import { useEffect, useRef, useState } from "react";
import type { Block } from "../lib/fold";
import { useStreamRate, type StreamRate } from "../lib/useStreamRate";
import { MarkdownBody } from "./MarkdownBody";
import { ThinkingBlock } from "./ThinkingBlock";
import { ToolCard } from "./ToolCard";

// The transcript.
//
// Auto-scroll follows the end of the content only while the reader is already
// there: scrolling up to read something must not be undone by the next token,
// which is the single most common way a streaming window becomes unusable.
export function ChatWindow({
  blocks,
  running,
  onFork,
  canFork,
}: {
  blocks: Block[];
  running: boolean;
  onFork?: () => void;
  canFork?: boolean;
}) {
  const scroll = useRef<HTMLDivElement>(null);
  const pinned = useRef(true);
  const stream = useStreamRate(blocks, running);
  // The turn whose copy just succeeded, so only that button says "已复制"
  // instead of every one of them at once.
  const [copied, setCopied] = useState<number | null>(null);
  const said = useRef<number | undefined>(undefined);
  const turns = group(blocks);

  useEffect(() => {
    const node = scroll.current;
    if (!node || !pinned.current) return;
    node.scrollTop = node.scrollHeight;
  }, [blocks]);

  useEffect(() => () => window.clearTimeout(said.current), []);

  const onScroll = () => {
    const node = scroll.current;
    if (!node) return;
    const distance = node.scrollHeight - node.scrollTop - node.clientHeight;
    pinned.current = distance < 48;
  };

  const copy = async (turn: number, text: string) => {
    if (!(await copyText(text))) return;
    setCopied(turn);
    window.clearTimeout(said.current);
    said.current = window.setTimeout(() => setCopied(null), 1600);
  };

  if (blocks.length === 0) {
    return (
      <div className="chat__scroll" ref={scroll}>
        <div className="chat__empty">
          <h1>atlas</h1>
          <p>问一个问题开始，或从左侧选一段对话。</p>
        </div>
      </div>
    );
  }

  return (
    <div className="chat__scroll" ref={scroll} onScroll={onScroll}>
      <div className="chat__column">
        {turns.map((turn, index) => {
          const streaming = running && index === turns.length - 1;
          return (
            <div className="turn" key={index}>
              {turn.map((block) => (
                <BlockView key={block.id} block={block} />
              ))}
              {hasAnswer(turn) ? (
                // Under the message it takes rather than at the end of the
                // conversation: every answer can be lifted out on its own, and
                // the reader does not have to scroll past the rest to reach the
                // one they want.
                <div className="turn__actions">
                  <button
                    type="button"
                    className="copy-button"
                    onClick={() => void copy(index, answerOf(turn))}
                    // Only the turn in flight is half-written. The answers above
                    // it are finished, so they stay copyable while it streams.
                    disabled={streaming}
                    title={streaming ? "这一轮还在跑，等它写完再复制" : "把这一条回答复制到剪贴板"}
                  >
                    <CopyMark />
                    {copied === index ? "已复制" : "复制"}
                  </button>
                </div>
              ) : null}
            </div>
          );
        })}
        {stream ? <StreamHint rate={stream} /> : null}
        {running ? <span className="sr-only">running</span> : null}
        {onFork && canFork ? (
          // The fork stays at the end of the conversation because that is the
          // point it acts on: it continues from the last thing said. Copy left
          // this row when it became a control of every message.
          <div className="chat__actions">
            <button
              type="button"
              className="fork-button"
              onClick={onFork}
              disabled={running}
              title="复制这段对话，开一条新分支"
            >
              <ForkMark />
              分叉
            </button>
          </div>
        ) : null}
      </div>
    </div>
  );
}

// answerOf is what a copy button puts on the clipboard: the answers of one
// turn, and nothing else.
//
// Not the conversation and not the turn either. Tool cards are how the answer
// was reached rather than what it says, so pasting a JSON argument dump into a
// message is the failure this filter prevents; thinking is the model talking to
// itself, notices and receipts are the window talking to the reader, and the
// prompt is the reader's own words. The answers are joined back together
// because a turn that called a tool twice is still one message.
function answerOf(turn: Block[]): string {
  return turn
    .filter((block) => block.role === "answer")
    .map((block) => block.text.trim())
    .filter(Boolean)
    .join("\n\n");
}

// hasAnswer decides whether the turn draws a copy control at all. It asks the
// same question answerOf answers without building the string, because this runs
// for every turn on every token and a conversation is long by the time the
// reader is scrolling back through it.
function hasAnswer(turn: Block[]): boolean {
  return turn.some((block) => block.role === "answer" && block.text.trim() !== "");
}

// copyText hands a string to the system clipboard.
//
// The async clipboard is the one to want, but the window runs in a webview that
// may serve the page from a scheme it does not treat as secure, where the API is
// simply absent — so the old selection-based copy stays as the fallback rather
// than leaving a button that does nothing in the shipped app.
async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    // Fall through: a refused permission is not a reason to give up on copy.
  }
  const area = document.createElement("textarea");
  area.value = text;
  area.setAttribute("readonly", "");
  area.style.position = "fixed";
  area.style.top = "-1000px";
  document.body.appendChild(area);
  area.select();
  try {
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    document.body.removeChild(area);
  }
}

// ForkMark is the branch glyph: one line that splits into two, drawn rather than
// typed because no font carries a fork and a missing glyph would leave an empty
// button.
function ForkMark() {
  return (
    <svg className="fork-button__mark" viewBox="0 0 16 16" width="14" height="14" aria-hidden="true" focusable="false">
      <path d="M4.6 4.6v6.8" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" fill="none" />
      <path
        d="M4.6 8.4c0-2.1 1.5-2.9 3.3-2.9h2.7"
        stroke="currentColor"
        strokeWidth="1.4"
        strokeLinecap="round"
        fill="none"
      />
      <circle cx="4.6" cy="3.3" r="1.5" fill="currentColor" />
      <circle cx="4.6" cy="12.7" r="1.5" fill="currentColor" />
      <circle cx="11.9" cy="5.5" r="1.5" fill="currentColor" />
    </svg>
  );
}

// CopyMark is two sheets, the near one offset so the far one reads as a second
// copy behind it. Drawn for the same reason as ForkMark.
function CopyMark() {
  return (
    <svg className="copy-button__mark" viewBox="0 0 16 16" width="14" height="14" aria-hidden="true" focusable="false">
      <rect
        x="2.6"
        y="2.6"
        width="8.2"
        height="9.6"
        rx="1.6"
        stroke="currentColor"
        strokeWidth="1.4"
        fill="none"
      />
      <path
        d="M5.4 1.2h6.4a1.6 1.6 0 0 1 1.6 1.6v7.4"
        stroke="currentColor"
        strokeWidth="1.4"
        strokeLinecap="round"
        fill="none"
        opacity="0.55"
      />
    </svg>
  );
}

// BlockView is one item of a transcript, in whatever the block turned out to be.
//
// 导出给侧面板：那边也是同一批块，只是没有轮次、没有复制按钮。第二套实现会随
// 着这里多一种角色而变成「主对话认识、侧对话不认识」，而两者读的是同一份折叠。
export function BlockView({ block }: { block: Block }) {
  switch (block.role) {
    case "user":
      return <div className="bubble">{block.text}</div>;
    case "answer":
      return <MarkdownBody text={block.text} />;
    case "thinking":
      return <ThinkingBlock block={block} />;
    case "tool":
      return <ToolCard block={block} />;
    case "notice":
      // A notice is a sentence with a rule on either side of it. /help writes a
      // table instead, and a centred flex row would run its columns together
      // into one line, so anything with a newline in it gets the block form.
      return block.text.includes("\n") ? (
        <div className="notice notice--block">{block.text}</div>
      ) : (
        <div className="notice">
          <span className="notice__rule" />
          <span>{block.text}</span>
          <span className="notice__rule" />
        </div>
      );
    case "error":
      return <div className="notice notice--error">{block.text}</div>;
    case "receipt":
      return <div className="receipt">{block.text}</div>;
    default:
      return (
        <div className="notice">
          <span className="notice__rule" />
          <span>{block.text}</span>
          <span className="notice__rule" />
        </div>
      );
  }
}

// StreamHint is the live rate, drawn where the receipt will land: the same slot
// answers "how fast is it going" during the turn and "what did it cost" after
// it, so the eye does not have to look in two places for one fact.
function StreamHint({ rate }: { rate: StreamRate }) {
  const tokens = rate.tokens >= 1000 ? `${(rate.tokens / 1000).toFixed(1)}k` : String(rate.tokens);
  const tone = rate.rate >= 50 ? "fast" : rate.rate >= 15 ? "steady" : "slow";
  return (
    <div className="stream-hint" title="流式估算，轮末的收据才是真实用量">
      <span className={`stream-hint__dot stream-hint__dot--${tone}`} />
      <span>
        ~{tokens} tok · {rate.rate.toFixed(1)} t/s
      </span>
    </div>
  );
}

// group starts a new turn at every prompt. A notice belongs to the turn above
// it, which is why only "user" opens one.
function group(blocks: Block[]): Block[][] {
  const turns: Block[][] = [];
  for (const block of blocks) {
    if (block.role === "user" || turns.length === 0) {
      turns.push([]);
    }
    turns[turns.length - 1].push(block);
  }
  return turns;
}
