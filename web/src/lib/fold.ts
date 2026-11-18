// The fold: agent events in, blocks out.
//
// This mirrors internal/transcript's role split — user, answer, thinking, tool,
// notice, error — because the window and the TUI must agree on what counts as
// one tool call. What is duplicated here is only the shape of a block; the
// kernel's transcript package owns the authoritative fold for anything that
// gets written back.
//
// Blocks are updated in place by id rather than rebuilt, so a streaming answer
// appends to the last block instead of re-rendering the transcript.

import type { AgentEvent, TranscriptBlock } from "../api";

export type Role = "user" | "answer" | "thinking" | "tool" | "notice" | "error" | "receipt";

export interface Block {
  id: number;
  role: Role;
  name?: string;
  text: string;
  /** One line beside the marker: an argument preview, a duration, an outcome. */
  note?: string;
  /** Shown when open; replaced by the result when it lands. */
  progress?: string;
  /** The call's arguments in full, for the card a reader opens. */
  args?: string;
  /** What the call returned, in full. */
  result?: string;
  callId?: string;
  open: boolean;
  error?: boolean;
}

let nextId = 1;

export function blank(role: Role, init: Partial<Block> = {}): Block {
  return { id: nextId++, role, text: "", open: false, ...init };
}

// What a card carries when it opens. A tool that read a megabyte is not one the
// page should hold: the drawer is where a whole file belongs.
const ARGS_LIMIT = 4000;
const RESULT_LIMIT = 20000;

/** trim keeps a JSON preview to one readable line. */
function trim(value: unknown, limit = 120): string {
  if (value === undefined || value === null) return "";
  let text: string;
  try {
    text = typeof value === "string" ? value : JSON.stringify(value);
  } catch {
    text = String(value);
  }
  if (!text) return "";
  return text.length > limit ? `${text.slice(0, limit)}…` : text;
}

/** expand keeps the body a reader opened the card for: JSON with its newlines,
 *  not the one line that is exactly what the preview already said. */
function expand(value: unknown, limit: number): string {
  if (value === undefined || value === null) return "";
  let text: string;
  if (typeof value === "string") {
    text = value;
  } else {
    try {
      text = JSON.stringify(value, null, 2);
    } catch {
      text = String(value);
    }
  }
  return text.length > limit ? `${text.slice(0, limit)}…` : text;
}

/** resultText unwraps a tool result the way the kernel's own fold does. The
 *  live event carries the whole `tool.Result` envelope (`content` blocks plus
 *  details), and printing the envelope would show a reader JSON around the one
 *  sentence the tool actually said. */
function resultText(value: unknown): string {
  if (value !== null && typeof value === "object" && "content" in value) {
    const content = (value as { content?: unknown }).content;
    if (Array.isArray(content)) {
      const texts = content
        .map((part) =>
          part !== null && typeof part === "object" && "text" in part
            ? String((part as { text?: unknown }).text ?? "")
            : "",
        )
        .filter((text) => text.length > 0);
      if (texts.length > 0) return expand(texts.join("\n"), RESULT_LIMIT);
    }
  }
  return expand(value, RESULT_LIMIT);
}


function closeOpen(blocks: Block[]): Block[] {
  return blocks.map((block) => (block.open ? { ...block, open: false } : block));
}

/** update replaces one block by id, leaving every other reference intact. */
function update(blocks: Block[], id: number, patch: Partial<Block>): Block[] {
  return blocks.map((block) => (block.id === id ? { ...block, ...patch } : block));
}

function openBlock(blocks: Block[], role: Role, init: Partial<Block> = {}): [Block[], Block] {
  const block = blank(role, { ...init, open: true });
  return [[...blocks, block], block];
}

function lastOpen(blocks: Block[], role: Role): Block | undefined {
  for (let index = blocks.length - 1; index >= 0; index -= 1) {
    const block = blocks[index];
    if (block.role === role) {
      return block.open ? block : undefined;
    }
  }
  return undefined;
}

export interface FoldResult {
  blocks: Block[];
  /** A turn-level fact the shell shows outside the transcript. */
  notice?: { role: "notice" | "error"; text: string };
}

/** apply folds one agent event into the transcript. */
export function apply(blocks: Block[], event: AgentEvent): FoldResult {
  switch (event.type) {
    case "message_start":
      return { blocks };

    case "message_update": {
      const inner = event.assistantMessageEvent;
      if (!inner) return { blocks };

      if (inner.type === "text_start") {
        const [next] = openBlock(closeOpen(blocks), "answer");
        return { blocks: next };
      }
      if (inner.type === "text_delta") {
        const open = lastOpen(blocks, "answer");
        if (open) {
          return { blocks: update(blocks, open.id, { text: open.text + (inner.delta ?? "") }) };
        }
        // A delta with no start still has an owner: the block it belongs to is
        // created here rather than dropped, because losing the first word of an
        // answer is worse than an extra block.
        const [next, block] = openBlock(closeOpen(blocks), "answer", { text: inner.delta ?? "" });
        return { blocks: update(next, block.id, {}) };
      }
      if (inner.type === "text_end") {
        return { blocks: closeOpen(blocks) };
      }
      if (inner.type === "thinking_start") {
        const [next] = openBlock(blocks, "thinking");
        return { blocks: next };
      }
      if (inner.type === "thinking_delta") {
        const open = lastOpen(blocks, "thinking");
        if (open) {
          return { blocks: update(blocks, open.id, { text: open.text + (inner.delta ?? "") }) };
        }
        const [next] = openBlock(blocks, "thinking", { text: inner.delta ?? "" });
        return { blocks: next };
      }
      if (inner.type === "thinking_end") {
        return { blocks: blocks.map((b) => (b.role === "thinking" && b.open ? { ...b, open: false } : b)) };
      }
      return { blocks };
    }

    case "tool_execution_start": {
      const [next] = openBlock(closeOpen(blocks), "tool", {
        name: event.toolName ?? "tool",
        callId: event.toolCallId,
        note: trim(event.args),
        args: expand(event.args, ARGS_LIMIT),
      });
      return { blocks: next };
    }

    case "tool_execution_update": {
      const open = lastOpen(blocks, "tool");
      if (!open) return { blocks };
      return { blocks: update(blocks, open.id, { progress: trim(event.partialResult, 160) }) };
    }

    case "tool_execution_end": {
      const open = lastOpen(blocks, "tool");
      const seconds =
        event.durationMs && event.durationMs > 0 ? `${(event.durationMs / 1000).toFixed(1)}s` : "";
      const outcome = event.isError ? "failed" : "done";
      const note = [outcome, seconds].filter(Boolean).join(" · ");
      if (!open) {
        const block = blank("tool", {
          name: event.toolName ?? "tool",
          note,
          error: event.isError,
          result: resultText(event.result),
        });
        return { blocks: [...blocks, block] };
      }
      return {
        blocks: update(blocks, open.id, {
          open: false,
          progress: undefined,
          note,
          error: event.isError,
          result: resultText(event.result),
        }),
      };
    }

    case "compaction_start": {
      const why = event.reason === "overflow" ? "上下文溢出" : event.reason === "manual" ? "手动" : "接近上限";
      const [next] = openBlock(closeOpen(blocks), "notice", {
        text: `正在压缩上下文（${why}）…`,
      });
      return { blocks: next };
    }

    case "compaction_end": {
      const next = closeOpen(blocks);
      const text = event.errorMessage
        ? `压缩失败：${withoutCompactionLabel(event.errorMessage)}`
        : event.aborted
          ? "压缩已中止"
          : `上下文已压缩：${compactionTokens(event.result)}`;
      return { blocks: [...next, blank("notice", { text })] };
    }

    case "agent_end":
      return { blocks: closeOpen(blocks) };

    default:
      return { blocks };
  }
}

/**
 * compactionTokens is what the compaction measured: how much went in, and the
 * estimate of what is left.
 *
 * The terminal prints the same pair ("compacted 120000 tokens into 30000"), and
 * a window that said only "compacted" would leave the reader guessing whether it
 * did anything at all — the second half of the pair is the whole point of asking
 * for it. The payload rides on the event's result key, the same unmarshaled
 * wire.CompactionResult the tool events use, so nothing here has to be taught a
 * second shape.
 */
function compactionTokens(value: unknown): string {
  if (value === null || typeof value !== "object") return "";
  const payload = value as { tokensBefore?: unknown; estimatedTokensAfter?: unknown };
  const before = typeof payload.tokensBefore === "number" ? payload.tokensBefore : 0;
  if (before <= 0) return "";
  const after = typeof payload.estimatedTokensAfter === "number" ? payload.estimatedTokensAfter : 0;
  const count = (tokens: number) => tokens.toLocaleString("en-US");
  return after > 0 ? `${count(before)} → ${count(after)} tokens` : `${count(before)} → ? tokens`;
}

/**
 * withoutCompactionLabel drops the kernel's own English label from a failed
 * compaction's message.
 *
 * The kernel builds the message as "<label>: <cause>", where the label is
 * "Compaction failed" for a manual one and "Auto-compaction failed" for the
 * threshold trigger — it is the terminal's wording, and the terminal prints it
 * verbatim. The window says the same thing in Chinese one line above, so keeping
 * the label would report one failure as "压缩失败：Compaction failed: …".
 *
 * A message with no known label is passed through untouched: this is a display
 * rule, and a kernel that changes its wording should show up as a longer line,
 * not as a swallowed error.
 */
function withoutCompactionLabel(message: string): string {
  for (const label of ["Compaction failed: ", "Auto-compaction failed: "]) {
    if (message.startsWith(label)) return message.slice(label.length);
  }
  return message;
}

/** notice appends a shell-level line into the transcript. */export function notice(blocks: Block[], role: "notice" | "error", text: string): Block[] {
  return [...blocks, blank(role, { text })];
}

/**
 * receipt appends what the turn cost under the turn it belongs to.
 *
 * It is a block rather than a status-bar fact because it is about the turn that
 * just ended, not about now: the terminal prints the same line as a note, and a
 * reader scrolling back should find it where the answer is. A turn that spent
 * nothing arrives with no fields at all and leaves no line behind.
 */
export function receipt(blocks: Block[], fields: string[]): Block[] {
  return fields.length === 0 ? blocks : [...blocks, blank("receipt", { text: fields.join(" · ") })];
}

/** prompt appends the reader's own message. */
export function prompt(blocks: Block[], text: string): Block[] {
  return [...blocks, blank("user", { text })];
}

/** settle stops every open block, which is what the end of a turn means. */
export function settle(blocks: Block[]): Block[] {
  return closeOpen(blocks);
}

/**
 * replay rebuilds the view of a conversation that already happened.
 *
 * The roles and the call/result pairing are decided on the server, by the same
 * fold the terminal uses: a window that re-derived them from the wire form
 * would be a second opinion on what counts as one tool call. This only gives
 * each item the local id the renderer keys on.
 */
export function replay(items: TranscriptBlock[]): Block[] {
  return items.map((item) => {
    const role = (ROLES.has(item.role) ? item.role : "notice") as Role;
    return blank(role, {
      name: item.name,
      text: item.text ?? "",
      note: item.note,
      args: item.args,
      result: item.result,
      callId: item.id,
      error: role === "error" || (role === "tool" && item.note?.includes("failed") === true),
    });
  });
}

const ROLES = new Set<string>(["user", "answer", "thinking", "tool", "notice", "error"]);
