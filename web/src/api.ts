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

/** One recorded fact of the conversation in view, mirroring httpapi.TrajectoryRow.
 *
 * 与 TranscriptBlock 的区别就是这张表存在的理由：thinking、附件正文、用量与
 * 压缩都在这里，而且 result 不截断——要查的那半句往往就在被切掉的那一半里。 */
export interface TrajectoryRow {
  index: number;
  seq: number;
  entryId: string;
  parentId?: string;
  turn: number;
  kind: string;
  timestamp: string;
  durationMs?: number;
  isError?: boolean;
  interrupted?: boolean;
  tool?: string;
  ref?: string;
  text?: string;
  thinking?: string;
  error?: string;
  args?: string;
  result?: string;
  model?: string;
  usage?: Usage;
  tokensBefore?: number;
}

/** Token and cost accounting, mirroring wire.Usage. All fields are always sent,
 *  so a zero here is a measured zero rather than a missing value. */
export interface Usage {
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
  cacheWrite1h?: number;
  reasoning?: number;
  totalTokens: number;
  cost: { input: number; output: number; cacheRead: number; cacheWrite: number; total: number };
}

/** One place a conversation can be branched before, mirroring httpapi.ForkPoint.
 *
 * Index 0 is the end of the conversation and carries no entryId: that is how the
 * route spells "the whole conversation", and it keeps the end a row in the same
 * list as every prompt rather than a command of its own. */
export interface ForkPoint {
  entryId?: string;
  index: number;
  text?: string;
}

/** What a fork left behind: the message that was branched off, if any. */
export interface ForkResult {
  ok: boolean;
  text?: string;
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

/** One selectable permission posture. The label and note come from the server so
 * the window and the terminal's /permission menu name the same posture the same
 * way. */
export interface PermissionMode {
  value: string;
  label: string;
  note?: string;
}

/** The gate's posture plus the whole menu, for the same reason ModelList carries
 * the catalog: a sheet with four rows and no current one cannot draw its own
 * selection. */
export interface PermissionState {
  mode: string;
  modes: PermissionMode[];
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
  /** What the turn spent, in the terminal's own words: tokens, rate, duration,
   *  end time. Absent when the turn spent nothing, or while it still runs. */
  receipt?: string[];
}

/** 侧对话。几乎与 Status 同形，但开着/关着是它自己的事实：主对话跑一轮不该
 *  把侧面板关掉，反过来也一样。 */
export interface SideStatus {
  open: boolean;
  running: boolean;
  error?: string;
  receipt?: string[];
}

/** 侧对话的全文。inherited 是开头有多少块继承自主对话（按块算，不按消息算，
 *  否则分隔线会落在一次工具调用的中间），继承来的那些不删掉：读者问侧对话
 *  「刚才说了什么」时，看到的该是模型拿到的同一段话。 */
export interface SideView {
  open: boolean;
  inherited: number;
  running: boolean;
  blocks: TranscriptBlock[];
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

  // 同一份已存日志的另一种读法：不截断、不去掉思考、连压缩与用量都在里面。
  // 同样不命名会话——回答的永远是屏幕上这一段。
  trajectory: () => call<TrajectoryRow[]>("/api/sessions/trajectory"),

  /** 端子那条 /compact。同步等它做完：压缩改的是下一轮要读的上下文，先返回
   *  就会让读者刚要求清掉的历史继续回答他的下一句。进度走事件流。 */
  compact: (note: string) => post<void>("/api/compact", { note }),

  /** 侧对话的全文与它的两条状态。侧对话只存在内存里，落盘的东西一样都没有。 */
  side: () => call<SideView>("/api/side"),

  openSide: () => post<SideView>("/api/side"),

  sideAsk: (prompt: string) => post<void>("/api/side/ask", { prompt }),

  closeSide: () => post<void>("/api/side/close"),

  // A conversation starts where the last one ran unless a folder is named:
  // the primary button keeps the reader where they are, a project's own button
  // moves them into it.
  newSession: (cwd?: string) => post<void>("/api/sessions/new", cwd ? { cwd } : undefined),

  // Where this conversation can be branched. The answer always describes the one
  // on screen, for the same reason the transcript names no session.
  forkPoints: () => call<ForkPoint[]>("/api/sessions/fork-points"),

  // fork copies the conversation into a new session and moves the kernel to it.
  // An empty entryId branches at the end, which is the whole conversation; a
  // named one keeps only what came before that message and hands it back as a
  // draft, because the reason to fork in front of something is to say it
  // differently. The name is optional: an empty string keeps the inherited one.
  forkSession: (entryId: string, name: string) =>
    post<ForkResult>("/api/sessions/fork", { entryId, name }),

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

  permission: () => call<PermissionState>("/api/permission"),

  /** Switches the posture; the answer is the state that took effect, so the sheet
   * never has to ask twice. */
  setPermission: (mode: string) => post<PermissionState>("/api/permission", { mode }),

  stats: () => call<Stats>("/api/stats"),

  files: (path = "") => call<DirListing>(`/api/files?path=${encodeURIComponent(path)}`),

  file: (path: string) => call<FileContent>(`/api/file?path=${encodeURIComponent(path)}`),
};
