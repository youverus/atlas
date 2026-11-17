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

export type Role = "user" | "answer" | "thinking" | "tool" | "notice" | "error";

export interface Block {
  id: number;
  role: Role;
  name?: string;
  text: string;
  /** One line beside the marker: an argument preview, a duration, an outcome. */
  note?: string;
  /** Shown when open; replaced by the result when it lands. */
  progress?: string;
  detail?: string;
  callId?: string;
  open: boolean;
  error?: boolean;
}

let nextId = 1;

export function blank(role: Role, init: Partial<Block> = {}): Block {
  return { id: nextId++, role, text: "", open: false, ...init };
}

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
          detail: event.isError ? trim(event.result, 2000) : "",
        });
        return { blocks: [...blocks, block] };
      }
      return {
        blocks: update(blocks, open.id, {
          open: false,
          progress: undefined,
          note,
          error: event.isError,
          detail: event.isError ? trim(event.result, 2000) : "",
        }),
      };
    }

    case "compaction_start": {
      const reason = event.reason === "overflow" ? "overflow" : event.reason || "threshold";
      const [next] = openBlock(closeOpen(blocks), "notice", {
        text: `compacting (${reason})…`,
      });
      return { blocks: next };
    }

    case "compaction_end": {
      const next = closeOpen(blocks);
      const block = blank("notice", {
        text: event.aborted ? "compaction aborted" : "context compacted",
      });
      return { blocks: [...next, block] };
    }

    case "agent_end":
      return { blocks: closeOpen(blocks) };

    default:
      return { blocks };
  }
}

/** notice appends a shell-level line into the transcript. */
export function notice(blocks: Block[], role: "notice" | "error", text: string): Block[] {
  return [...blocks, blank(role, { text })];
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
      detail: item.detail,
      callId: item.id,
      error: role === "error" || (role === "tool" && item.note?.includes("failed") === true),
    });
  });
}

const ROLES = new Set<string>(["user", "answer", "thinking", "tool", "notice", "error"]);
