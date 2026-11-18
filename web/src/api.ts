// The whole client/server contract, in one file.
//
// There is no generated client and no schema: the transport is five routes for
// a conversation plus a handful for the shell, and a type per route is shorter
// and more honest than a code generator over the same handful.

export interface Facts {
  version?: string;
  directory?: string;
  model?: string;
}

export interface PendingQuestion {
  id: string;
  subject: string;
  tool: string;
}

export interface State extends Facts {
  running: boolean;
  pending?: PendingQuestion[];
}

export interface SessionInfo {
  path: string;
  id: string;
  name?: string;
  cwd?: string;
  created?: string;
  modified?: string;
  messages: number;
  firstMessage?: string;
  archived?: boolean;
  current?: boolean;
}

/** One drawn item of a replayed conversation, mirroring httpapi.TranscriptBlock. */
export interface TranscriptBlock {
  role: string;
  name?: string;
  text?: string;
  note?: string;
  detail?: string;
  args?: string;
  result?: string;
  id?: string;
}

/** One folder in the rail, with the conversations whose header records it. */
export interface ProjectGroup {
  path: string;
  name: string;
  /** The folder conversations start in when the reader has picked nothing. */
  default?: boolean;
  /** The directory is gone; its conversations are still listed. */
  missing?: boolean;
  sessions: SessionInfo[];
}

export interface ProjectList {
  groups: ProjectGroup[];
}

export interface ModelOption {
  provider: string;
  id: string;
  name?: string;
  reasoning?: boolean;
  contextWindow?: number;
  levels?: string[];
  current?: boolean;
}

export interface ModelList {
  current: string;
  thinking?: string;
  levels?: string[];
  models: ModelOption[];
}

export interface Stats {
  sessionId?: string;
  sessionFile?: string;
  userMessages: number;
  assistantMessages: number;
  toolCalls: number;
  totalMessages: number;
  inputTokens: number;
  outputTokens: number;
  totalTokens: number;
  cost: number;
  contextTokens: number;
  contextWindow: number;
}

export interface FileEntry {
  name: string;
  path: string;
  isDir: boolean;
  size?: number;
}

export interface DirListing {
  path: string;
  entries: FileEntry[];
}

export interface FileContent {
  path: string;
  text: string;
  size: number;
  binary?: boolean;
  truncated?: boolean;
}

/** One assistant stream event, mirroring wire.AssistantEvent. */
export interface AssistantEvent {
  type: string;
  delta?: string;
  content?: string;
  contentIndex?: number;
  error?: { content?: string };
}

/** One agent lifecycle event, mirroring wire.AgentEvent. */
export interface AgentEvent {
  type: string;
  message?: { role?: string; content?: unknown };
  assistantMessageEvent?: AssistantEvent;
  toolCallId?: string;
  toolName?: string;
  args?: unknown;
  partialResult?: unknown;
  result?: unknown;
  isError?: boolean;
  durationMs?: number;
  reason?: string;
  aborted?: boolean;
  willRetry?: boolean;
  errorMessage?: string;
}

/** A status frame: the turn bracket the server sends around every turn. */
export interface Status {
  running: boolean;
  error?: string;
}

/** An answer the server refused, with the status kept: 501 from the folder
 * chooser is not a failure, it is this build saying it has none. */
export class ApiError extends Error {
  status: number;

  constructor(message: string, status: number) {
    super(message);
    this.status = status;
  }
}

async function call<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, init);
  if (!response.ok) {
    const detail = (await response.text()).trim();
    throw new ApiError(detail || `${response.status} ${response.statusText}`, response.status);
  }
  // 202 和 204 都表示「已接受、没有内容可读」：/api/ask 回 202 且响应体为空，
  // 直接按状态码判断会漏掉它，所以先读文本，只有真有内容才解析。
  const body = (await response.text()).trim();
  if (!body) {
    return undefined as T;
  }
  return JSON.parse(body) as T;
}

function post<T>(path: string, body?: unknown): Promise<T> {
  return call<T>(path, {
    method: "POST",
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
}

export const api = {
  state: () => call<State>("/api/state"),

  ask: (prompt: string) => post<void>("/api/ask", { prompt }),

  approve: (id: string, allowed: boolean, always: boolean) =>
    post<void>("/api/approval", { id, allowed, always }),

  sessions: (archived = false) =>
    call<SessionInfo[]>(`/api/sessions?archived=${archived ? "true" : "false"}`),

  openSession: (path: string) => post<void>("/api/sessions/open", { path }),

  // What the conversation in view holds. A switch has already happened by the
  // time this is asked, so it names no session: the answer is always the one on
  // screen, and a path parameter would let a stale row describe another one.
  transcript: () => call<TranscriptBlock[]>("/api/sessions/transcript"),

  // A conversation starts where the last one ran unless a folder is named:
  // the primary button keeps the reader where they are, a project's own button
  // moves them into it.
  newSession: (cwd?: string) => post<void>("/api/sessions/new", cwd ? { cwd } : undefined),

  archiveSession: (path: string) => post<void>("/api/sessions/archive", { path }),

  restoreSession: (path: string) => post<void>("/api/sessions/restore", { path }),

  deleteSession: (path: string) => post<void>("/api/sessions/delete", { path }),

  projects: (archived = false) =>
    call<ProjectList>(`/api/projects?archived=${archived ? "true" : "false"}`),

  addProject: (path: string) => post<void>("/api/projects", { path }),

  removeProject: (path: string) => post<void>("/api/projects/remove", { path }),

  /** Opens the shell's folder chooser. Rejects with 501 when this build has none. */
  pickFolder: () => call<{ path?: string }>("/api/projects/pick", { method: "POST" }),

  models: () => call<ModelList>("/api/models"),

  setModel: (provider: string, id: string) => post<void>("/api/models", { provider, id }),

  setThinking: (level: string) => post<void>("/api/thinking", { level }),

  stats: () => call<Stats>("/api/stats"),

  files: (path = "") => call<DirListing>(`/api/files?path=${encodeURIComponent(path)}`),

  file: (path: string) => call<FileContent>(`/api/file?path=${encodeURIComponent(path)}`),
};
