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
  session?: string;
  subject: string;
  tool: string;
}

export interface State extends Facts {
  deviceId?: string;
  session?: string;
  running: boolean;
  turnId?: string;
  runningSessions?: string[];
  archived?: boolean;
  pending?: PendingQuestion[];
}

export interface SessionInfo {
  path: string;
  id: string;
  name?: string;
  cwd?: string;
  deviceId?: string;
  created?: string;
  modified?: string;
  messages: number;
  firstMessage?: string;
  archived?: boolean;
  current?: boolean;
}

/** Compact metadata receipt for a versioned session artifact. */
export interface ArtifactRef {
  id: string;
  kind: "html" | "svg" | "markdown" | "code" | "csv" | string;
  title?: string;
  description?: string;
  language?: string;
  contentType?: string;
  version: number;
  bytes?: number;
  sha256?: string;
  action?: string;
  summary?: string;
  sourcePath?: string;
}

export interface ArtifactSummary {
  id: string;
  kind: "html" | "svg" | "markdown" | "code" | "csv" | string;
  title: string;
  description?: string;
  language?: string;
  contentType: string;
  version: number;
  bytes: number;
  sha256: string;
  createdAt: number;
  updatedAt: number;
}

export interface ArtifactVersion {
  version: number;
  title?: string;
  description?: string;
  kind: string;
  language?: string;
  contentType: string;
  bytes: number;
  sha256: string;
  sourcePath?: string;
  createdAt: number;
  action?: string;
  summary?: string;
}

export interface ArtifactDetail extends ArtifactSummary {
  activeVersion: number;
  content: string;
  versions: ArtifactVersion[];
}

export interface ArtifactExportResult {
  path: string;
  bytes: number;
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
  timestamp?: number;
  durationMs?: number;
  tokensPerSec?: number;
  subagent?: SubagentUpdate;
  artifact?: ArtifactRef;
  errorCode?: string;
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

/** One place a conversation can be branched from: after an answer, mirroring
 * httpapi.ForkPoint.
 *
 * Index 0 is the end of the conversation and carries no entryId: that is how the
 * route spells "the whole conversation", and it keeps the end a row in the same
 * list as every answer rather than a command of its own.
 *
 * Turn is the prompt the answer closed, counted from 1. The transcript the page
 * draws carries no entry ids, so this is how a button under a reply finds its
 * branch point. */
export interface ForkPoint {
  entryId?: string;
  index: number;
  turn?: number;
  text?: string;
}

/** What a fork left behind. Nothing: the answer that was kept is in the new
 *  session already, so there is no prompt to hand back for editing. */
export interface ForkResult {
  ok: boolean;
}

/** One folder in the rail, with the conversations whose header records it. */
export interface ProjectGroup {
  path: string;
  name: string;
  deviceId?: string;
  deviceName?: string;
  deviceState?: "offline" | "connecting" | "online" | "error" | string;
  remoteError?: string;
  /** The folder conversations start in when the reader has picked nothing. */
  default?: boolean;
  /** The directory is gone; its conversations are still listed. */
  missing?: boolean;
  sessions: SessionInfo[];
}

export interface BrowseDirEntry {
  name: string;
  path: string;
}

export interface DirBrowseResult {
  deviceId?: string;
  path: string;
  parent?: string;
  home?: string;
  entries: BrowseDirEntry[];
}

export interface DiscoveredProject {
  deviceId?: string;
  path: string;
  name: string;
  sessionCount?: number;
  lastModified?: string;
  added?: boolean;
}

export interface DiscoverProjectsResult {
  deviceId?: string;
  projects: DiscoveredProject[];
}

export interface DeviceInfo {
  id: string;
  name: string;
  host: string;
  port?: number;
  user?: string;
  identityFile?: string;
  remoteBin?: string;
  status: "offline" | "connecting" | "online" | "error" | string;
  error?: string;
  remoteOs?: string;
  remoteArch?: string;
  remoteVersion?: string;
  remoteHome?: string;
  lastConnected?: string;
  logs: string[];
}

export interface DeviceInput {
  name: string;
  host: string;
  port?: number;
  user?: string;
  identityFile?: string;
  remoteBin?: string;
}

export interface DeviceList {
  configPath?: string;
  sshHosts: string[];
  devices: DeviceInfo[];
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
  pending?: string;
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

export interface CompatConfig {
  maxTokensField?: string;
  requireReasoningContent?: boolean;
  disableStreamOptions?: boolean;
  tolerateMissingFinishReason?: boolean;
  requireToolResultName?: boolean;
  requireAssistantAfterToolResult?: boolean;
  version?: string;
  thinkingDialect?: string;
  disableReasoningEffort?: boolean;
  sendStore?: boolean;
  toolChoice?: string;
  adaptiveThinking?: boolean;
  strictTools?: boolean;
  serviceTier?: string;
  verbosity?: string;
  truncation?: string;
}

export interface ModelItemConfig {
  id: string;
  name?: string;
  baseUrl?: string;
  contextWindow?: number;
  maxTokens?: number;
  reasoning?: boolean;
  thinkingLevels?: string[];
  input?: string[];
  userAgent?: string;
}

export interface ProviderConfig {
  name: string;
  api: string;
  baseUrl?: string;
  apiKey?: string;
  apiKeyEnv?: string;
  apiKeyEnvSet?: boolean;
  userAgent?: string;
  compat?: CompatConfig;
  models: ModelItemConfig[];
}

export interface SubagentModelConfig {
  defaultModel?: string;
  explorer?: string;
  verifier?: string;
}

export interface ModelConfig {
  configPath?: string;
  defaultModel: string;
  thinkingLevel?: string;
  providers: ProviderConfig[];
  subagents?: SubagentModelConfig;
}

export interface MCPToolInfo {
  name: string;
  title?: string;
  wireName: string;
  description?: string;
}

export interface MCPServerInfo {
  name: string;
  version?: string;
  transport?: string;
  state: "connected" | "needs_login" | "failed" | "disabled" | string;
  declared: number;
  tools: MCPToolInfo[];
  missing?: string[];
  error?: string;
  refreshError?: string;
}

export interface MCPState {
  configPath?: string;
  servers: MCPServerInfo[];
  canToggle?: boolean;
}

export interface SkillRootInfo {
  scope: string;
  dir: string;
}

export interface SkillInfo {
  name: string;
  description?: string;
  path: string;
  scope: string;
  disabled?: boolean;
}

export interface SkillState {
  cwd?: string;
  roots: SkillRootInfo[];
  skills: SkillInfo[];
  notes?: string[];
  canToggle?: boolean;
}

export interface SkillDetail extends SkillInfo {
  body: string;
}

export interface ModelUsage {
  provider: string;
  model: string;
  totalTokens: number;
  tokensPerSec: number | null;
}

export interface UsageHistory {
  models: ModelUsage[];
  days: Record<string, number>;
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
  mcpServers?: number;
  mcpConfigured?: number;
  mcpTools?: number;
  mcpTokens?: number;
  skills?: number;
  skillsTokens?: number;
  agentsMdLoaded?: boolean;
  contextFiles?: string[];
  contextFilesTokens?: number;
  systemPrompt?: string;
  systemPromptTokens?: number;
}

export interface FileEntry {
  name: string;
  path: string;
  isDir: boolean;
  size?: number;
}

export interface DirListing {
  root?: string;
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

/** One background task snapshot, mirroring wire.SubagentUpdate. */
export interface SubagentUpdate {
  id: string;
  sessionId?: string;
  sessionFile?: string;
  toolCallId?: string;
  role?: string;
  description?: string;
  state: string;
  model?: string;
  provider?: string;
  report?: string;
  error?: string;
  durationMs?: number;
  usage?: Usage;
}

/** One agent lifecycle event, mirroring wire.AgentEvent. */
export interface AgentEvent {
  session?: string;
  type: string;
  message?: {
    role?: string;
    content?: unknown;
    timestamp?: number;
    durationMs?: number;
    stopReason?: string;
    errorMessage?: string;
    errorCode?: string;
  };
  assistantMessageEvent?: AssistantEvent;
  subagent?: SubagentUpdate;
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
export interface BrowserTabInfo {
  id: string;
  targetId: string;
  url: string;
  title: string;
  generation: number;
  loading: boolean;
  canGoBack: boolean;
  canGoForward: boolean;
  control: "user" | "agent" | string;
  paused?: boolean;
  active: boolean;
  createdAt: number;
}

export interface BrowserState {
  available: boolean;
  executable?: string;
  profileDir?: string;
  running: boolean;
  activeTab?: string;
  tabs: BrowserTabInfo[];
}

export interface BrowserCapture {
  tabId: string;
  mime: string;
  base64: string;
  width: number;
  height: number;
}

export interface PluginInfo {
  name: string;
  description?: string;
  dir: string;
  skillsDir?: string;
  skillNames: string[];
  servers: string[];
}

export interface PluginState {
  dir?: string;
  plugins: PluginInfo[];
  notes?: string[];
}

export interface Status {
  session?: string;
  turnId?: string;
  running: boolean;
  aborted?: boolean;
  error?: string;
  receipt?: string[];
  endedAt?: number;
  durationMs?: number;
  tokensPerSec?: number;
}

/** 侧对话。几乎与 Status 同形，但开着/关着是它自己的事实：主对话跑一轮不该
 *  把侧面板关掉，反过来也一样。 */
export interface SideStatus extends Status {
  open: boolean;
}

/** 侧对话的全文。inherited 是开头有多少块继承自主对话（按块算，不按消息算，
 *  否则分隔线会落在一次工具调用的中间），继承来的那些不删掉：读者问侧对话
 *  「刚才说了什么」时，看到的该是模型拿到的同一段话。 */
export interface SideView {
  open: boolean;
  inherited: number;
  running: boolean;
  turnId?: string;
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

export interface AttachmentPayload {
  kind: "image" | "pdf" | "text";
  name: string;
  mimeType?: string;
  data: string;
}

export interface TerminalSessionInfo {
  id: string;
  shell?: string;
  cwd?: string;
}

/** 本机连接信息：浏览器扩展的 options 页要贴的 URL 与 token。token 由桌面端
 *  签发并跨重启稳定；token 为空表示本构建没有签发扩展凭据。 */
export interface Connection {
  url: string;
  token?: string;
}

export const api = {
  state: () => call<State>("/api/state"),

  connection: () => call<Connection>("/api/connection"),

  stop: (turnId: string, session?: string, side = false) => post<void>("/api/stop", { turnId, session, side }),

  ask: (prompt: string, session?: string, attachments?: AttachmentPayload[]) => {
    const payload: { prompt: string; session?: string; attachments?: AttachmentPayload[] } = { prompt };
    if (session) payload.session = session;
    if (attachments && attachments.length > 0) payload.attachments = attachments;
    return post<void>("/api/ask", payload);
  },

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
  compact: (note: string, session?: string) =>
    post<void>("/api/compact", session ? { note, session } : { note }),

  /** 侧对话的全文与它的两条状态。侧对话只存在内存里，落盘的东西一样都没有。 */
  side: () => call<SideView>("/api/side"),

  openSide: () => post<SideView>("/api/side"),

  sideAsk: (prompt: string, session?: string) =>
    post<void>("/api/side/ask", session ? { prompt, session } : { prompt }),

  closeSide: () => post<void>("/api/side/close"),

  // A conversation starts where the last one ran unless a folder is named:
  // the primary button keeps the reader where they are, a project's own button
  // moves them into it.
  newSession: (cwd?: string, deviceId?: string) => {
    const body: { cwd?: string; deviceId?: string } = {};
    if (cwd) body.cwd = cwd;
    if (deviceId && deviceId !== "local") body.deviceId = deviceId;
    return post<void>("/api/sessions/new", Object.keys(body).length > 0 ? body : undefined);
  },

  // Where this conversation can be branched. The answer always describes the one
  // on screen, for the same reason the transcript names no session.
  forkPoints: () => call<ForkPoint[]>("/api/sessions/fork-points"),

  // fork copies the conversation into a new session and moves the kernel to it.
  // An empty entryId branches at the end, which is the whole conversation; a
  // named one keeps everything up to and including that answer. The name is
  // optional: an empty string keeps the inherited one.
  forkSession: (entryId: string, name: string) =>
    post<ForkResult>("/api/sessions/fork", { entryId, name }),

  archiveSession: (path: string) => post<void>("/api/sessions/archive", { path }),

  restoreSession: (path: string) => post<void>("/api/sessions/restore", { path }),

  deleteSession: (path: string) => post<void>("/api/sessions/delete", { path }),

  projects: (archived = false) =>
    call<ProjectList>(`/api/projects?archived=${archived ? "true" : "false"}`),

  addProject: (path: string, name = "", deviceId = "") => {
    const payload: { path: string; name?: string; deviceId?: string } = { path };
    if (name.trim()) payload.name = name.trim();
    if (deviceId && deviceId !== "local") payload.deviceId = deviceId;
    return post<void>("/api/projects", payload);
  },

  removeProject: (path: string, deviceId = "") =>
    post<void>(
      "/api/projects/remove",
      deviceId && deviceId !== "local" ? { path, deviceId } : { path },
    ),

  browseDirs: (deviceId = "", path = "", includeHidden = false) => {
    const params = new URLSearchParams();
    if (deviceId && deviceId !== "local") params.set("device", deviceId);
    if (path) params.set("path", path);
    if (includeHidden) params.set("hidden", "true");
    const qs = params.toString();
    return call<DirBrowseResult>(qs ? `/api/projects/browse?${qs}` : "/api/projects/browse");
  },

  discoverProjects: (deviceId = "") => {
    const qs =
      deviceId && deviceId !== "local" ? `?device=${encodeURIComponent(deviceId)}` : "";
    return call<DiscoverProjectsResult>(`/api/projects/discover${qs}`);
  },

  /** Opens the shell's folder chooser. Rejects with 501 when this build has none. */
  pickFolder: () => call<{ path?: string }>("/api/projects/pick", { method: "POST" }),

  devices: () => call<DeviceList>("/api/devices"),

  createDevice: (input: DeviceInput) => post<DeviceInfo>("/api/devices", input),

  updateDevice: (id: string, input: DeviceInput) =>
    call<DeviceInfo>(`/api/devices/${encodeURIComponent(id)}`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(input),
    }),

  deleteDevice: (id: string) =>
    call<{ ok: boolean }>(`/api/devices/${encodeURIComponent(id)}`, { method: "DELETE" }),

  connectDevice: (id: string) =>
    post<DeviceInfo>(`/api/devices/${encodeURIComponent(id)}/connect`),

  disconnectDevice: (id: string) =>
    post<DeviceInfo>(`/api/devices/${encodeURIComponent(id)}/disconnect`),

  syncDeviceConfig: (id: string) =>
    post<{ ok: boolean }>(`/api/devices/${encodeURIComponent(id)}/sync-config`),

  /** Creates an embedded interactive PTY session in the active session's directory. */
  createTerminal: (cols: number, rows: number) =>
    post<TerminalSessionInfo>("/api/terminal/create", { cols, rows }),

  writeTerminal: (id: string, data: string, encoding?: "base64") =>
    post<{ ok: boolean }>("/api/terminal/write", encoding ? { id, data, encoding } : { id, data }),

  resizeTerminal: (id: string, cols: number, rows: number) =>
    post<{ ok: boolean }>("/api/terminal/resize", { id, cols, rows }),

  closeTerminal: (id: string) => post<{ ok: boolean }>("/api/terminal/close", { id }),

  models: () => call<ModelList>("/api/models"),

  setModel: (provider: string, id: string) => post<void>("/api/models", { provider, id }),

  setThinking: (level: string) => post<void>("/api/thinking", { level }),

  permission: () => call<PermissionState>("/api/permission"),

  /** Switches the posture; the answer is the state that took effect, so the sheet
   * never has to ask twice. */
  setPermission: (mode: string) => post<PermissionState>("/api/permission", { mode }),

  reloadConfig: () => post<{ ok: boolean }>("/api/config/reload"),

  modelConfig: () => call<ModelConfig>("/api/config/models"),

  saveModelConfig: (cfg: ModelConfig) => post<{ ok: boolean }>("/api/config/models", cfg),

  mcp: () => call<MCPState>("/api/mcp"),

  refreshMCP: (name = "") => post<MCPState>("/api/mcp/refresh", name ? { name } : undefined),

  setMCPEnabled: (name: string, enabled: boolean) => post<MCPState>("/api/mcp/enabled", { name, enabled }),

  skills: () => call<SkillState>("/api/skills"),

  setSkillEnabled: (name: string, enabled: boolean) => post<SkillState>("/api/skills/enabled", { name, enabled }),

  skillContent: (name: string) =>
    call<SkillDetail>(`/api/skills/content?name=${encodeURIComponent(name)}`),

  loadSkillPrompt: (name: string, args = "") =>
    post<{ prompt: string }>("/api/skills/load", { name, args }),

  plugins: () => call<PluginState>("/api/plugins"),

  browser: () => call<BrowserState>("/api/browser"),

  browserOpen: (url: string) => post<BrowserTabInfo>("/api/browser/open", { url }),

  browserNavigate: (tabId: string, url: string) =>
    post<BrowserTabInfo>("/api/browser/navigate", { tabId, url }),

  browserAction: (tabId: string, action: string) =>
    post<BrowserState>("/api/browser/action", { tabId, action }),

  browserScreenshot: (tabId = "") =>
    call<BrowserCapture>(`/api/browser/screenshot?tabId=${encodeURIComponent(tabId)}`),

  stats: () => call<Stats>("/api/stats"),

  usageHistory: (timeZone: string, signal: AbortSignal) =>
    call<UsageHistory>(`/api/usage-history?${new URLSearchParams({ timeZone })}`, { signal }),

  files: (path = "", signal?: AbortSignal) =>
    call<DirListing>(`/api/files?path=${encodeURIComponent(path)}`, { signal }),

  file: (path: string) => call<FileContent>(`/api/file?path=${encodeURIComponent(path)}`),

  artifacts: () => call<ArtifactSummary[]>("/api/artifacts"),

  artifact: (id: string, version = 0) =>
    call<ArtifactDetail>(
      `/api/artifacts/${encodeURIComponent(id)}${version > 0 ? `?v=${version}` : ""}`,
    ),

  deleteArtifact: (id: string) =>
    call<{ ok: boolean }>(`/api/artifacts/${encodeURIComponent(id)}`, { method: "DELETE" }),

  exportArtifact: (id: string, version: number, path = "") =>
    post<ArtifactExportResult>(`/api/artifacts/${encodeURIComponent(id)}/export`, {
      version,
      path,
    }),

  remote: () => call<RemoteStatus>("/api/remote"),

  saveFeishu: (cfg: FeishuConfigUpdate) =>
    call<RemoteStatus>("/api/remote/feishu", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(cfg),
    }),

  startRemote: () => post<RemoteStatus>("/api/remote/start"),

  stopRemote: () => post<RemoteStatus>("/api/remote/stop"),

  approvePairing: (code: string) => post<RemoteStatus>("/api/remote/approve", { code }),

  allowRemoteUser: (adapter: string, userId: string) =>
    post<RemoteStatus>("/api/remote/allowlist", { adapter, userId }),

  revokeRemoteUser: (adapter: string, userId: string) =>
    post<RemoteStatus>("/api/remote/allowlist/remove", { adapter, userId }),

  unbindRemoteChat: (key: string) =>
    post<RemoteStatus>("/api/remote/bindings/remove", { key }),

  schedules: () => call<ScheduleList>("/api/schedules"),

  createSchedule: (input: ScheduleInput) => post<ScheduledTask>("/api/schedules", input),

  updateSchedule: (id: string, input: ScheduleInput) =>
    call<ScheduledTask>(`/api/schedules/${encodeURIComponent(id)}`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(input),
    }),

  deleteSchedule: (id: string) =>
    call<{ ok: boolean }>(`/api/schedules/${encodeURIComponent(id)}`, {
      method: "DELETE",
    }),

  runScheduleNow: (id: string) =>
    post<ScheduledTask>(`/api/schedules/${encodeURIComponent(id)}/run`),

  // 桌面宠物双击时请壳把主窗口带回前台；只有桌面壳注入了这个能力，其余
  // 构建回 501，页面把它当作无操作即可。
  focusShell: () => post<void>("/api/shell/focus"),

  // 桌面宠物开关的读与设：501 表示这个构建没有壳（浏览器/headless），
  // 设置页据此隐藏该项。设置是幂等的声明式写入（回 204），关闭窗口的
  // 拆除是异步的，所以页面直接采用自己请求的状态——壳最终收敛到它。
  shellPet: () => call<{ visible: boolean }>("/api/shell/pet"),
  setShellPet: (visible: boolean) => post<void>("/api/shell/pet", { visible }),
};

export interface FeishuSettingsView {
  enabled: boolean;
  appId: string;
  hasSecret: boolean;
  secretMasked?: string;
  secretMode?: "literal" | "env" | "unset" | string;
  envOverride?: boolean;
  dmPolicy: string;
  groupPolicy: string;
  requireMention: boolean;
  allowBots: boolean;
  cwd?: string;
  admins: string[];
  allowedUsers: string[];
}

export interface FeishuConfigUpdate {
  enabled: boolean;
  appId: string;
  appSecret?: string;
  clearSecret?: boolean;
  dmPolicy: string;
  groupPolicy: string;
  requireMention: boolean;
  allowBots: boolean;
  cwd?: string;
  admins: string[];
  allowedUsers: string[];
  startAfterSave?: boolean;
}

export interface RemotePairingCode {
  code: string;
  adapter: string;
  userId: string;
  chatId: string;
  created: string;
}

export interface RemoteAllowlistEntry {
  adapter: string;
  userId: string;
}

export interface RemoteBinding {
  key: string;
  name?: string;
  cwd?: string;
  session?: string;
  created?: string;
}

export interface RemoteStatus {
  state: "stopped" | "starting" | "running" | "error" | string;
  connection?: string;
  lastError?: string;
  startedAt?: string;
  configPath?: string;
  defaultCwd?: string;
  feishu: FeishuSettingsView;
  pendingCodes: RemotePairingCode[];
  runtimeAllowlist: RemoteAllowlistEntry[];
  bindings: RemoteBinding[];
  recentLogs?: string[];
}

export interface ScheduleInput {
  name: string;
  enabled: boolean;
  prompt: string;
  model?: string;
  cwd?: string;
  sessionMode: "new" | "continue";
  scheduleType: "daily" | "interval";
  hour: number;
  minute: number;
  weekdays?: number[];
  intervalMinutes?: number;
}

export interface ScheduledTask extends ScheduleInput {
  id: string;
  running: boolean;
  lastSession?: string;
  lastRunAt?: string;
  nextRunAt?: string;
  lastStatus?: "" | "success" | "error" | string;
  lastError?: string;
  lastSummary?: string;
  lastDurationMs?: number;
  runCount?: number;
  createdAt?: string;
  updatedAt?: string;
}

export interface ScheduleList {
  defaultCwd?: string;
  tasks: ScheduledTask[];
}
