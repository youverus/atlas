import { useCallback, useEffect, useId, useRef, useState } from "react";
import type { ForkPoint } from "../api";
import { collectTurnArtifacts } from "../lib/artifacts";
import { copyText } from "../lib/clipboard";
import { subagentFailed, subagentPending, type Block } from "../lib/fold";
import { ArtifactCards } from "./ArtifactCard";
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
  forkPoints,
  onFork,
  forkingEntryId,
  activeArtifactId,
  onOpenArtifact,
}: {
  blocks: Block[];
  running: boolean;
  /** Where this conversation can be branched: one point per answer. The kernel
   *  decides what a point is; this window only draws the buttons alongside
   *  copy, which is why the list is passed in rather than derived here. */
  forkPoints?: ForkPoint[];
  onFork?: (entryId: string) => void;
  forkingEntryId?: string | null;
  activeArtifactId?: string;
  onOpenArtifact?: (id: string, version?: number) => void;
}) {
  const scroll = useRef<HTMLDivElement>(null);
  const pinned = useRef(true);
  const [navState, setNavState] = useState({ canTop: false, canPrevUser: false, canBottom: false });
  // The turn whose copy just succeeded, so only that button says "已复制"
  // instead of every one of them at once.
  const [copied, setCopied] = useState<number | null>(null);
  const said = useRef<number | undefined>(undefined);
  const turns = group(blocks);
  // The prompt each turn belongs to, counted from 1. It is not the array index:
  // group starts a turn at the first block whatever it is, so a conversation that
  // opens with a notice would otherwise offer its first answer the second turn's
  // branch point. A turn with no prompt of its own keeps the number before it,
  // which is the turn it continues.
  const numbers = promptNumbers(turns);
  const pointOf = (turn: number) => forkPoints?.find((point) => point.turn === turn);

  const findPrevUserTarget = useCallback((node: HTMLDivElement): number | null => {
    const containerTop = node.getBoundingClientRect().top;
    const prompts = node.querySelectorAll<HTMLElement>('[data-user-message="true"]');
    let target: number | null = null;
    for (const el of prompts) {
      const top = Math.max(0, el.getBoundingClientRect().top - containerTop + node.scrollTop - 16);
      if (top < node.scrollTop - 8) {
        target = top;
      } else {
        break;
      }
    }
    return target;
  }, []);

  const syncNavState = useCallback(() => {
    const node = scroll.current;
    if (!node) {
      setNavState((prev) =>
        !prev.canTop && !prev.canPrevUser && !prev.canBottom
          ? prev
          : { canTop: false, canPrevUser: false, canBottom: false },
      );
      return;
    }
    const distance = node.scrollHeight - node.scrollTop - node.clientHeight;
    const canTop = node.scrollTop > 8;
    const canPrevUser = findPrevUserTarget(node) !== null;
    const canBottom = distance > 24;
    setNavState((prev) =>
      prev.canTop === canTop && prev.canPrevUser === canPrevUser && prev.canBottom === canBottom
        ? prev
        : { canTop, canPrevUser, canBottom },
    );
  }, [findPrevUserTarget]);

  useEffect(() => {
    const node = scroll.current;
    if (!node) return;
    if (pinned.current) {
      node.scrollTop = node.scrollHeight;
    }
    syncNavState();
  }, [blocks, syncNavState]);

  useEffect(() => {
    const node = scroll.current;
    if (!node || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(() => syncNavState());
    observer.observe(node);
    const column = node.firstElementChild;
    if (column) observer.observe(column);
    return () => observer.disconnect();
  }, [blocks.length, syncNavState]);

  useEffect(() => () => window.clearTimeout(said.current), []);

  const onScroll = () => {
    const node = scroll.current;
    if (!node) return;
    const distance = node.scrollHeight - node.scrollTop - node.clientHeight;
    pinned.current = distance < 48;
    syncNavState();
  };

  const scrollToTop = () => {
    const node = scroll.current;
    if (!node) return;
    pinned.current = false;
    node.scrollTo({ top: 0, behavior: "smooth" });
  };

  const scrollToPrevUser = () => {
    const node = scroll.current;
    if (!node) return;
    const target = findPrevUserTarget(node);
    if (target === null) return;
    pinned.current = false;
    node.scrollTo({ top: target, behavior: "smooth" });
  };

  const scrollToBottom = () => {
    const node = scroll.current;
    if (!node) return;
    pinned.current = true;
    node.scrollTo({ top: node.scrollHeight, behavior: "smooth" });
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
          <EngravedFMark />
          <p className="chat__empty-prompt">问一个问题开始，或从左侧选一段对话。</p>
        </div>
      </div>
    );
  }

  return (
    <div className="chat__viewport">
      <div className="chat__scroll" ref={scroll} onScroll={onScroll}>
        <div className="chat__column">
          {turns.map((turn, index) => {
            const streaming = running && index === turns.length - 1;
            const point = pointOf(numbers[index]);
            const forking = Boolean(point?.entryId) && forkingEntryId === point?.entryId;
            const firstAgentIndex = turn.findIndex((block) => block.role !== "user");
            const userBlocks = firstAgentIndex === -1 ? turn : turn.slice(0, firstAgentIndex);
            const agentBlocks = firstAgentIndex === -1 ? [] : turn.slice(firstAgentIndex);
            const turnArtifacts = collectTurnArtifacts(agentBlocks);
            const meta = !streaming && hasAgentContent(agentBlocks) ? turnTiming(agentBlocks) : null;
            const showActions = hasAnswer(turn) || Boolean(point?.entryId && onFork) || Boolean(meta);
            return (
              <div className="turn" key={index}>
                {userBlocks.map((block) => (
                  <BlockView key={block.id} block={block} />
                ))}
                {agentBlocks.length > 0 || streaming ? (
                  <div className="turn__agent">
                    {renderTurnItems(agentBlocks).map((item) =>
                      item.kind === "process" ? (
                        <TurnProcess key={`process-${item.key}`} blocks={item.blocks} streaming={streaming} />
                      ) : (
                        <BlockView key={item.block.id} block={item.block} />
                      ),
                    )}
                    {streaming && !hasTrailingVisibleAnswer(agentBlocks) ? (
                      <WaitingStatus
                        startedAt={userBlocks.at(-1)?.startedAt}
                        working={agentBlocks.some((block) => block.open && block.role !== "answer")}
                      />
                    ) : null}
                    {onOpenArtifact ? (
                      <ArtifactCards
                        artifacts={turnArtifacts}
                        activeId={activeArtifactId}
                        onOpen={onOpenArtifact}
                      />
                    ) : null}
                    {showActions ? (
                      // Under the message it takes rather than at the end of the
                      // conversation: every answer can be lifted out on its own, and
                      // the reader does not have to scroll past the rest to reach the
                      // one they want.
                      <div className="turn__actions">
                        {hasAnswer(turn) ? (
                          <button
                            type="button"
                            className="copy-button"
                            data-copied={copied === index ? "true" : undefined}
                            data-tooltip={
                              copied === index
                                ? "已复制"
                                : streaming
                                  ? "这一轮还在跑，等它写完再复制"
                                  : "复制"
                            }
                            onClick={() => void copy(index, answerOf(turn))}
                            // Only the turn in flight is half-written. The answers above
                            // it are finished, so they stay copyable while it streams.
                            disabled={streaming}
                            aria-label={copied === index ? "已复制" : "复制"}
                          >
                            {copied === index ? <CheckMark /> : <CopyMark />}
                            <span className="sr-only">{copied === index ? "已复制" : "复制"}</span>
                          </button>
                        ) : null}
                        {point?.entryId && onFork ? (
                          // The branch is taken after this answer, so it continues
                          // from what the agent just said instead of re-asking the
                          // question. The turn in flight has no entry to branch from
                          // yet: the kernel lists a point only once its reply is in.
                          <button
                            type="button"
                            className="fork-button"
                            data-tooltip={forking ? "分叉中…" : "从这里分叉"}
                            disabled={running || forking}
                            onClick={() => onFork(point.entryId as string)}
                            aria-label={forking ? "分叉中…" : "从这里分叉"}
                          >
                            <ForkMark />
                            <span className="sr-only">{forking ? "分叉中…" : "从这里分叉"}</span>
                          </button>
                        ) : null}
                        {meta ? (
                          <span className="turn__meta" title={meta.title}>
                            {meta.label}
                          </span>
                        ) : null}
                      </div>
                    ) : null}
                  </div>
                ) : null}
              </div>
            );
          })}
          {running ? <span className="sr-only">running</span> : null}
        </div>
      </div>

      <div className="chat__nav" role="group" aria-label="会话导航">
        <button
          type="button"
          className="chat__nav-btn"
          disabled={!navState.canTop}
          onClick={scrollToTop}
          aria-label="回到会话最开始"
          title="回到会话最开始"
        >
          <DoubleUpMark />
        </button>
        <button
          type="button"
          className="chat__nav-btn"
          disabled={!navState.canPrevUser}
          onClick={scrollToPrevUser}
          aria-label="回到上一条用户消息"
          title="回到上一条用户消息"
        >
          <UpMark />
        </button>
        <button
          type="button"
          className="chat__nav-btn"
          disabled={!navState.canBottom}
          onClick={scrollToBottom}
          aria-label="回到最近的消息"
          title="回到最近的消息"
        >
          <DownMark />
        </button>
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

// promptNumbers numbers the turns by the prompt that opened them, counted from
// 1, which is the number the kernel puts on a fork point. A turn without a
// prompt of its own (a notice folded in before the first question, say) keeps
// the number before it, because it belongs to that stretch of the conversation.
function promptNumbers(turns: Block[][]): number[] {
  let n = 0;
  return turns.map((turn) => (turn.some((block) => block.role === "user") ? (n += 1) : n));
}

// hasAnswer decides whether the turn draws a copy control at all. It asks the
// same question answerOf answers without building the string, because this runs
// for every turn on every token and a conversation is long by the time the
// reader is scrolling back through it.
function hasAnswer(turn: Block[]): boolean {
  return turn.some((block) => block.role === "answer" && block.text.trim() !== "");
}

// ForkMark follows zcode's TrendingUpDown fork glyph: a timeline path splitting
// into two forward-branching arrows.
function ForkMark() {
  return (
    <svg
      className="fork-button__mark"
      viewBox="0 0 24 24"
      width="14"
      height="14"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.9"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      <path d="M14.828 14.828 21 21" />
      <path d="M21 16v5h-5" />
      <path d="m21 3-9 9-4-4-6 6" />
      <path d="M21 8V3h-5" />
    </svg>
  );
}

function CheckMark() {
  return (
    <svg
      className="copy-button__mark"
      viewBox="0 0 24 24"
      width="14"
      height="14"
      fill="none"
      stroke="currentColor"
      strokeWidth="2.1"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      <path d="M20 6 9 17l-5-5" />
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

function DoubleUpMark() {
  return (
    <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true" focusable="false">
      <path
        d="M4.2 7.2 8 3.4l3.8 3.8M4.2 11.8 8 8l3.8 3.8"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
        fill="none"
      />
    </svg>
  );
}

function UpMark() {
  return (
    <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true" focusable="false">
      <path
        d="M8 12.6V3.6M4.4 7.1 8 3.5l3.6 3.6"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
        fill="none"
      />
    </svg>
  );
}

function DownMark() {
  return (
    <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true" focusable="false">
      <path
        d="M8 3.4v9M4.4 8.9 8 12.5l3.6-3.6"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
        fill="none"
      />
    </svg>
  );
}

// EngravedFMark draws a recessed geometric "F" watermark directly into the
// empty conversation surface, modeled after zcode's etched Z.svg backdrop.
function EngravedFMark() {
  const rawId = useId();
  const idPrefix = rawId.replace(/:/g, "");
  const fillGradId = `${idPrefix}-fill`;
  const strokeGradId = `${idPrefix}-stroke`;
  const blurFilterId = `${idPrefix}-blur`;
  const path =
    "M56 24H224L202 68H104V138H190L168 182H104V296H56V24Z";

  return (
    <svg
      className="chat__empty-mark"
      viewBox="0 0 280 320"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
      aria-hidden="true"
      focusable="false"
    >
      <g filter={`url(#${blurFilterId})`}>
        <path d={path} fill={`url(#${fillGradId})`} />
      </g>
      <path d={path} stroke={`url(#${strokeGradId})`} strokeWidth="1.25" />
      <defs>
        <filter
          id={blurFilterId}
          x="52"
          y="20"
          width="176"
          height="280"
          filterUnits="userSpaceOnUse"
          colorInterpolationFilters="sRGB"
        >
          <feFlood floodOpacity="0" result="BackgroundImageFix" />
          <feBlend mode="normal" in="SourceGraphic" in2="BackgroundImageFix" result="shape" />
          <feGaussianBlur stdDeviation="1.2" result="blur" />
        </filter>
        <linearGradient
          id={fillGradId}
          x1="140"
          y1="24"
          x2="140"
          y2="296"
          gradientUnits="userSpaceOnUse"
        >
          <stop offset="0" stopColor="var(--fx-engraved-fill-top)" />
          <stop offset="0.55" stopColor="var(--fx-engraved-fill-mid)" />
          <stop offset="1" stopColor="var(--fx-engraved-fill-bottom)" />
        </linearGradient>
        <linearGradient
          id={strokeGradId}
          x1="140"
          y1="24"
          x2="140"
          y2="296"
          gradientUnits="userSpaceOnUse"
        >
          <stop offset="0" stopColor="var(--fx-engraved-stroke-top)" />
          <stop offset="0.55" stopColor="var(--fx-engraved-stroke-mid)" />
          <stop offset="1" stopColor="var(--fx-engraved-stroke-bottom)" />
        </linearGradient>
      </defs>
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
      return (
        <div className="bubble" data-user-message="true">
          {block.text}
        </div>
      );
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
      return null;
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

function hasAgentContent(blocks: Block[]): boolean {
  return blocks.some(
    (block) => block.role === "answer" || block.role === "thinking" || block.role === "tool" || block.role === "error",
  );
}

function turnTiming(blocks: Block[]): { label: string; title: string } | null {
  let endedAt: number | undefined;
  let durationMs: number | undefined;
  let tokensPerSec: number | undefined;
  for (let i = blocks.length - 1; i >= 0; i -= 1) {
    const block = blocks[i];
    if (endedAt === undefined && block.endedAt && block.endedAt > 0) {
      endedAt = block.endedAt;
    }
    if (durationMs === undefined && block.durationMs !== undefined && block.durationMs >= 0) {
      durationMs = block.durationMs;
    }
    if (tokensPerSec === undefined && block.tokensPerSec !== undefined && block.tokensPerSec > 0) {
      tokensPerSec = block.tokensPerSec;
    }
    if (endedAt !== undefined && durationMs !== undefined && tokensPerSec !== undefined) break;
  }
  if (endedAt === undefined && durationMs === undefined && tokensPerSec === undefined) {
    return null;
  }

  const labelParts: string[] = [];
  const titleParts: string[] = [];
  if (endedAt !== undefined) {
    const formatted = formatCompletionTime(endedAt);
    labelParts.push(`完成于 ${formatted.short}`);
    titleParts.push(`完成时间 ${formatted.full}`);
  }
  if (durationMs !== undefined) {
    const dur = formatDuration(durationMs);
    labelParts.push(`用时 ${dur}`);
    titleParts.push(`用时 ${dur}`);
  }
  if (tokensPerSec !== undefined) {
    const rate = formatTokenRate(tokensPerSec);
    labelParts.push(rate);
    titleParts.push(`速率 ${rate}`);
  }
  return {
    label: labelParts.join(" · "),
    title: titleParts.join(" · "),
  };
}

function formatTokenRate(value: number): string {
  return value >= 10 ? `${Math.round(value)} tok/s` : `${value.toFixed(1)} tok/s`;
}

function formatCompletionTime(ms: number): { short: string; full: string } {
  const date = new Date(ms);
  const now = new Date();
  const pad = (value: number) => String(value).padStart(2, "0");
  const hhmmss = `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
  const fullDate = `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
  const sameDay =
    date.getFullYear() === now.getFullYear() &&
    date.getMonth() === now.getMonth() &&
    date.getDate() === now.getDate();
  const short = sameDay ? hhmmss : `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${hhmmss}`;
  return { short, full: `${fullDate} ${hhmmss}` };
}

function formatDuration(ms: number): string {
  if (ms < 1000) {
    return `${Math.round(ms)}ms`;
  }
  if (ms < 60_000) {
    return `${(ms / 1000).toFixed(1)}s`;
  }
  const totalSeconds = Math.floor(ms / 1000);
  if (totalSeconds < 3600) {
    return `${Math.floor(totalSeconds / 60)}m${String(totalSeconds % 60).padStart(2, "0")}s`;
  }
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  return `${hours}h${String(minutes).padStart(2, "0")}m`;
}

function WaitingStatus({ startedAt, working }: { startedAt?: number; working: boolean }) {
  const [mountedAt] = useState(Date.now);
  const start = startedAt ?? mountedAt;
  const [now, setNow] = useState(Date.now);
  const seconds = Math.max(0, Math.floor((now - start) / 1000));

  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(id);
  }, []);

  return (
    <div className="agent-waiting" role="status" aria-live="polite">
      <svg className="agent-waiting__ring" viewBox="0 0 18 18" width="16" height="16" aria-hidden="true">
        <circle className="agent-waiting__track" cx="9" cy="9" r="6.5" fill="none" strokeWidth="2" />
        <circle
          className="agent-waiting__arc"
          cx="9"
          cy="9"
          r="6.5"
          fill="none"
          strokeWidth="2"
          strokeLinecap="round"
          strokeDasharray="16 26"
        />
      </svg>
      <span className="agent-waiting__text">
        {working ? "正在处理" : "正在等待模型回复"}
        <span aria-hidden="true"> · {seconds} 秒</span>
      </span>
    </div>
  );
}

function hasTrailingVisibleAnswer(turn: Block[]): boolean {
  let lastWorkIndex = -1;
  for (let i = 0; i < turn.length; i += 1) {
    const role = turn[i].role;
    if (role === "thinking" || role === "tool") {
      lastWorkIndex = i;
    }
  }
  for (let i = lastWorkIndex + 1; i < turn.length; i += 1) {
    const block = turn[i];
    if (block.role === "answer" && block.text.trim() !== "") {
      return true;
    }
  }
  return false;
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

type TurnRenderItem =
  | { kind: "block"; block: Block }
  | { kind: "process"; key: number; blocks: Block[] };

function renderTurnItems(turn: Block[]): TurnRenderItem[] {
  let lastWorkIndex = -1;
  for (let i = 0; i < turn.length; i += 1) {
    const role = turn[i].role;
    if (role === "thinking" || role === "tool") {
      lastWorkIndex = i;
    }
  }
  if (lastWorkIndex === -1) {
    return turn.map((block) => ({ kind: "block", block }));
  }

  let firstWorkIndex = -1;
  for (let i = 0; i <= lastWorkIndex; i += 1) {
    const block = turn[i];
    if (
      block.role === "thinking" ||
      block.role === "tool" ||
      (block.role === "answer" && i < lastWorkIndex && block.text.trim() !== "")
    ) {
      firstWorkIndex = i;
      break;
    }
  }
  if (firstWorkIndex === -1) {
    return turn.map((block) => ({ kind: "block", block }));
  }

  const items: TurnRenderItem[] = [];
  for (let i = 0; i < firstWorkIndex; i += 1) {
    items.push({ kind: "block", block: turn[i] });
  }

  const processBlocks: Block[] = [];
  const visibleDuringWork: Block[] = [];
  for (let i = firstWorkIndex; i <= lastWorkIndex; i += 1) {
    const block = turn[i];
    if (block.role === "thinking" || block.role === "tool") {
      processBlocks.push(block);
    } else if (block.role === "answer") {
      if (block.text.trim() !== "") {
        processBlocks.push(block);
      }
    } else {
      visibleDuringWork.push(block);
    }
  }

  if (processBlocks.length > 0) {
    items.push({ kind: "process", key: processBlocks[0].id, blocks: processBlocks });
  }
  for (const block of visibleDuringWork) {
    items.push({ kind: "block", block });
  }
  for (let i = lastWorkIndex + 1; i < turn.length; i += 1) {
    items.push({ kind: "block", block: turn[i] });
  }
  return items;
}

function TurnProcess({ blocks, streaming }: { blocks: Block[]; streaming: boolean }) {
  const [open, setOpen] = useState(false);

  const thinkingCount = blocks.filter((b) => b.role === "thinking").length;
  const toolBlocks = blocks.filter((b) => b.role === "tool");
  const failedCount = toolBlocks.filter((b) => b.error || (b.subagent && subagentFailed(b.subagent))).length;
  const pendingSubagents = toolBlocks.filter((b) => b.subagent && subagentPending(b.subagent)).length;
  const intermediateCount = blocks.filter((b) => b.role === "answer" && b.text.trim() !== "").length;
  const activeBlock = [...blocks].reverse().find((b) => b.open);
  const runningProcess = streaming && Boolean(activeBlock);

  const summaryParts: string[] = [];
  if (pendingSubagents > 0) {
    summaryParts.push(`${pendingSubagents} 项子代理进行中`);
  }
  if (thinkingCount > 0) {
    summaryParts.push(`${thinkingCount} 次思考`);
  }
  if (toolBlocks.length > 0) {
    summaryParts.push(`${toolBlocks.length} 次工具调用`);
  }
  if (intermediateCount > 0) {
    summaryParts.push(`${intermediateCount} 段过程说明`);
  }
  if (failedCount > 0) {
    summaryParts.push(`${failedCount} 项失败`);
  }

  const activeHint = runningProcess
    ? activeBlock?.role === "tool"
      ? `正在调用 ${activeBlock.name ?? "tool"}`
      : activeBlock?.role === "thinking"
        ? "思考中…"
        : "执行中…"
    : "";

  const label = runningProcess ? "正在执行" : toolBlocks.length > 0 ? "执行过程" : "思考过程";

  return (
    <div
      className="turn-process"
      data-open={open ? "true" : "false"}
      data-error={failedCount > 0 ? "true" : "false"}
    >
      <button
        type="button"
        className="turn-process__head"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
      >
        {runningProcess || pendingSubagents > 0 ? (
          <span className="spinner" aria-hidden="true" />
        ) : (
          <span className={failedCount > 0 ? "dot dot--danger" : "turn-process__dot"} aria-hidden="true" />
        )}
        <span className="turn-process__label">{label}</span>
        {summaryParts.length > 0 ? (
          <span className="turn-process__summary">{summaryParts.join(" · ")}</span>
        ) : null}
        {activeHint ? <span className="turn-process__active">{activeHint}</span> : null}
        <span className="turn-process__toggle">{open ? "收起" : "展开"}</span>
      </button>
      {open ? (
        <div className="turn-process__body">
          {blocks.map((block) =>
            block.role === "thinking" ? (
              <ThinkingBlock key={block.id} block={block} defaultOpen />
            ) : block.role === "answer" ? (
              <div className="turn-process__note" key={block.id}>
                <MarkdownBody text={block.text} />
              </div>
            ) : (
              <BlockView key={block.id} block={block} />
            ),
          )}
        </div>
      ) : null}
    </div>
  );
}
