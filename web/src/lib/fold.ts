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

import type { AgentEvent, ArtifactRef, SubagentUpdate, TranscriptBlock } from "../api";

export type Role = "user" | "answer" | "thinking" | "tool" | "notice" | "error" | "receipt";

const STOPPED_NOTICE = "已停止本轮";

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
  subagent?: SubagentUpdate;
  artifact?: ArtifactRef;
  open: boolean;
  error?: boolean;
  errorCode?: string;
  startedAt?: number;
  endedAt?: number;
  durationMs?: number;
  tokensPerSec?: number;
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

function subagentReceipt(result: unknown, callId?: string): SubagentUpdate | undefined {
  if (!result || typeof result !== "object") return undefined;
  const { details } = result as { details?: SubagentUpdate };
  if (details?.id && details.state === "queued" && details.toolCallId === callId) return details;
  return undefined;
}

export function isArtifactProducerTool(name?: string): boolean {
  return name === "artifact_publish" || name === "artifact_patch";
}

function artifactReceipt(
  result: unknown,
  isError?: boolean,
  aborted?: boolean,
  name?: string,
): ArtifactRef | undefined {
  if (isError || aborted || !isArtifactProducerTool(name)) return undefined;
  if (!result || typeof result !== "object") return undefined;
  const { details } = result as { details?: Partial<ArtifactRef> };
  if (!details || typeof details.id !== "string" || !details.id) return undefined;
  if (typeof details.kind !== "string" || !details.kind) return undefined;
  if (typeof details.version !== "number" || details.version <= 0) return undefined;
  return {
    id: details.id,
    kind: details.kind,
    ...(typeof details.title === "string" && details.title ? { title: details.title } : {}),
    ...(typeof details.description === "string" && details.description ? { description: details.description } : {}),
    ...(typeof details.language === "string" && details.language ? { language: details.language } : {}),
    ...(typeof details.contentType === "string" && details.contentType ? { contentType: details.contentType } : {}),
    version: details.version,
    ...(typeof details.bytes === "number" ? { bytes: details.bytes } : {}),
    ...(typeof details.sha256 === "string" && details.sha256 ? { sha256: details.sha256 } : {}),
    ...(typeof details.action === "string" && details.action ? { action: details.action } : {}),
    ...(typeof details.summary === "string" && details.summary ? { summary: details.summary } : {}),
    ...(typeof details.sourcePath === "string" && details.sourcePath ? { sourcePath: details.sourcePath } : {}),
  };
}

function closeOpen(blocks: Block[], except?: Role): Block[] {
  return blocks.map((block) => (block.open && block.role !== except ? { ...block, open: false } : block));
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

function toolCall(blocks: Block[], id?: string): Block | undefined {
  if (!id) return lastOpen(blocks, "tool");
  for (let i = blocks.length - 1; i >= 0; i -= 1) {
    const block = blocks[i];
    if (block.role === "tool" && block.callId === id) return block;
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

    case "message_end": {
      let next = closeOpen(blocks);
      const message = event.message;
      if (message?.role !== "assistant") return { blocks: next };
      if (message.stopReason !== "aborted" && (message.stopReason === "error" || message.errorMessage)) {
        next = [...next, blank("error", {
          text: modelError(message.errorMessage, message.errorCode),
          errorCode: message.errorCode,
        })];
      }
      return {
        blocks: stampLastTurnTiming(next, {
          endedAt: message.timestamp,
          durationMs: message.durationMs,
        }),
      };
    }

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
      const [next] = openBlock(closeOpen(blocks, "tool"), "tool", {
        name: event.toolName ?? "tool",
        callId: event.toolCallId,
        note: trim(event.args),
        args: expand(event.args, ARGS_LIMIT),
      });
      return { blocks: next };
    }

    case "tool_execution_update": {
      const call = toolCall(blocks, event.toolCallId);
      if (!call?.open) return { blocks };
      return { blocks: update(blocks, call.id, { progress: trim(event.partialResult, 160) }) };
    }

    case "tool_execution_end": {
      const call = toolCall(blocks, event.toolCallId);
      const seconds =
        event.durationMs && event.durationMs > 0 ? `${(event.durationMs / 1000).toFixed(1)}s` : "";
      const outcome = event.aborted ? "interrupted" : event.isError ? "failed" : "done";
      const name = event.toolName ?? call?.name ?? "tool";
      const patch = {
        open: false,
        progress: undefined,
        note: [outcome, seconds].filter(Boolean).join(" · "),
        error: event.isError && !event.aborted,
        result: resultText(event.result),
        subagent: name === "subagent" ? subagentReceipt(event.result, event.toolCallId) : undefined,
        artifact: artifactReceipt(event.result, event.isError, event.aborted, name),
      };
      if (call) return { blocks: update(blocks, call.id, patch) };
      const block = blank("tool", { ...patch, name, callId: event.toolCallId });
      return { blocks: [...blocks, block] };
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
      if (event.willRetry && next.at(-2)?.errorCode === "context_overflow") {
        next.splice(next.length - 2, 1);
      }
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

function modelError(detail = "", code = ""): string {
  const raw = detail.trim();
  const status = Number(/^([45]\d{2})(?:\s|:|$)/.exec(raw)?.[1] ?? 0);
  const summary = modelErrorSummary(raw, status, code);
  return status ? `HTTP ${status} · ${summary}` : summary;
}

function modelErrorSummary(detail: string, status: number, code: string): string {
  switch (true) {
    case code === "context_overflow":
    case /context[_ ](?:length[_ ]exceeded|overflow)|prompt (?:is )?too long|exceeds the context window/i.test(detail):
      return "消息超出模型的上下文上限。请缩短消息、移除部分附件，或换用上下文更大的模型。";
    case status === 402:
    case /insufficient[_ ](?:quota|credits|balance)|billing[_ ]hard[_ ]limit|quota exhausted|exceeded your current quota/i.test(detail):
      return "模型服务的额度或余额不足。请检查账户用量和计费设置。";
    case status === 401:
    case /authentication_error|invalid[_ ]api[_ ]key|missing api key|unauthenticated/i.test(detail):
      return "模型服务认证失败。请检查模型设置中的 API Key 是否正确或已过期。";
    case status === 403:
    case /permission_denied|permission_error|access_denied/i.test(detail):
      return "模型服务拒绝访问。请检查 API Key 是否有权限使用当前模型。";
    case status === 404:
    case /model_not_found/i.test(detail):
      return "找不到模型或 API 地址。请检查模型 ID 和服务地址。";
    case status === 429:
    case /rate[_ ]limit|too many requests|resource_exhausted|throttl/i.test(detail):
      return "模型请求达到频率限制。请稍后重试。";
    case code === "stream_idle_timeout":
    case /model response stream idle timeout/i.test(detail):
      return "模型服务已连接，但响应流长时间没有新内容。请重试或切换模型。";
    case status === 408 || status === 504:
    case /timed? out|timeout|deadline exceeded|deadline_exceeded/i.test(detail):
      return "等待模型服务响应超时。请检查网络和代理设置，或稍后重试。";
    case /no such host|name resolution/i.test(detail):
      return "无法解析模型服务地址。请检查服务地址、DNS 和代理设置。";
    case /tls handshake|x509:|certificate verify/i.test(detail):
      return "无法与模型服务建立安全连接。请检查服务证书和代理设置。";
    case /connect:|dial tcp|network is unreachable|connection refused|proxyconnect|no route to host/i.test(detail):
      return "无法连接模型服务。请检查网络、代理和服务地址后重试。";
    case /unexpected eof|connection reset|without (?:a )?(?:finish[_ ]reason|stop reason|a?\s*terminal event)|no stream chunks/i.test(detail):
      return "模型回复中途断开或未完整返回。请重试。";
    case /content_filter|content_policy_violation|safety filter/i.test(detail):
      return "模型服务拦截了这条请求。请调整消息内容后重试。";
    case status >= 500:
    case /overloaded[_ ]error|service unavailable|server_error/i.test(detail):
      return "模型服务暂时不可用。请稍后重试或切换模型。";
    case status === 413:
      return "请求内容过大。请缩短消息或移除部分附件后重试。";
    case status === 400 || status === 422:
    case /invalid[_ ]request|invalid_argument/i.test(detail):
      return "模型服务拒绝了请求格式。请检查模型配置及附件格式是否受支持。";
    default: {
      const description = detail.replace(/https?:\/\/[^\s"'<>]+/gi, "[服务地址]").replace(/\s+/g, " ");
      return description ? `模型调用失败。${trim(description, 240)}` : "模型调用失败。请重试或切换模型。";
    }
  }
}

/** notice appends a shell-level line into the transcript. */
export function notice(blocks: Block[], role: "notice" | "error", text: string): Block[] {
  return [...blocks, blank(role, { text })];
}

/**
 * finishTurn attaches completion timestamp and wall-clock duration to the last
 * turn's agent output without appending a visible token receipt block.
 */
export function finishTurn(
  blocks: Block[],
  timing?: { endedAt?: number; durationMs?: number; tokensPerSec?: number },
): Block[] {
  return stampLastTurnTiming(blocks, timing);
}

function stampLastTurnTiming(
  blocks: Block[],
  timing?: { endedAt?: number; durationMs?: number; tokensPerSec?: number },
): Block[] {
  let lastUserIdx = -1;
  for (let i = blocks.length - 1; i >= 0; i -= 1) {
    if (blocks[i].role === "user") {
      lastUserIdx = i;
      break;
    }
  }
  let targetIdx = -1;
  for (let i = blocks.length - 1; i > lastUserIdx; i -= 1) {
    const role = blocks[i].role;
    if (role === "answer" || role === "error") {
      targetIdx = i;
      break;
    }
    if (targetIdx === -1 && role !== "notice") {
      targetIdx = i;
    }
  }
  if (targetIdx === -1) return blocks;

  const current = blocks[targetIdx];
  const endedAt = timing?.endedAt && timing.endedAt > 0 ? timing.endedAt : current.endedAt ?? Date.now();
  const startedAt = lastUserIdx >= 0 ? blocks[lastUserIdx].startedAt : undefined;
  const durationMs =
    timing?.durationMs !== undefined && timing.durationMs >= 0
      ? timing.durationMs
      : current.durationMs !== undefined && current.durationMs >= 0
        ? current.durationMs
        : startedAt && endedAt >= startedAt
          ? endedAt - startedAt
          : undefined;
  const tokensPerSec =
    timing?.tokensPerSec !== undefined && timing.tokensPerSec > 0
      ? timing.tokensPerSec
      : current.tokensPerSec;

  return blocks.map((block, index) =>
    index === targetIdx
      ? {
          ...block,
          endedAt,
          ...(durationMs !== undefined ? { durationMs } : {}),
          ...(tokensPerSec !== undefined && tokensPerSec > 0 ? { tokensPerSec } : {}),
        }
      : block,
  );
}

/** prompt appends the reader's own message. */
export function prompt(blocks: Block[], text: string): Block[] {
  return [...blocks, blank("user", { text, startedAt: Date.now() })];
}

/** settle stops every open block, which is what the end of a turn means. */
export function settle(blocks: Block[]): Block[] {
  return closeOpen(blocks);
}

export function interrupt(blocks: Block[]): Block[] {
  const next = settle(blocks);
  const last = next.at(-1);
  if (last?.role === "notice" && last.text === STOPPED_NOTICE) return next;
  return notice(next, "notice", STOPPED_NOTICE);
}

export function subagentPending(task: SubagentUpdate): boolean {
  return task.state === "queued" || task.state === "running" || task.state === "stopping";
}

export function subagentFailed(task: SubagentUpdate): boolean {
  return task.state === "failed" || task.state === "lost";
}

/** Overlay live task snapshots on their parent tool rows, including replayed rows. */
export function withSubagents(
  blocks: Block[],
  tasks?: ReadonlyMap<string, SubagentUpdate>,
  fallbackTasks?: ReadonlyMap<string, SubagentUpdate>,
): Block[] {
  if (!tasks?.size && !fallbackTasks?.size) return blocks;
  return blocks.map((block) => {
    if (block.role !== "tool" || !block.callId) return block;
    const task = tasks?.get(block.callId) ?? fallbackTasks?.get(block.callId);
    if (!task) return block;
    const saved = block.subagent;
    if (saved && (saved.id !== task.id || (!subagentPending(saved) && subagentPending(task)))) {
      return block;
    }
    return { ...block, subagent: { ...saved, ...task } };
  });
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
  const tasks = new Map<string, SubagentUpdate>();
  const blocks: Block[] = [];
  for (const item of items) {
    if (item.role !== "tool" && item.subagent?.toolCallId) {
      tasks.set(item.subagent.toolCallId, item.subagent);
      continue;
    }
    const role = (ROLES.has(item.role) ? item.role : "notice") as Role;
    let text = item.text ?? "";
    if (role === "error") text = modelError(text, item.errorCode);
    if (role === "notice" && text === "interrupted") text = STOPPED_NOTICE;
    blocks.push(blank(role, {
      name: item.name,
      text,
      note: item.note,
      args: item.args,
      result: item.result,
      callId: item.id,
      subagent: item.subagent,
      ...(item.artifact ? { artifact: item.artifact } : {}),
      error: role === "error" || (role === "tool" && item.note?.includes("failed") === true),
      startedAt: role === "user" ? item.timestamp : undefined,
      endedAt: item.timestamp,
      durationMs: item.durationMs,
      tokensPerSec: item.tokensPerSec,
    }));
  }
  return withSubagents(blocks, tasks);
}

const ROLES = new Set<string>(["user", "answer", "thinking", "tool", "notice", "error"]);
