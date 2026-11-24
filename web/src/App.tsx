import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { CSSProperties } from "react";
import {
  api,
  ApiError,
  type ArtifactDetail,
  type ArtifactSummary,
  type AttachmentPayload,
  type ForkPoint,
  type ModelList,
  type PendingQuestion,
  type PermissionState,
  type ProjectGroup,
  type SessionInfo,
  type State,
  type Stats,
  type SubagentUpdate,
  type TrajectoryRow,
} from "./api";
import { ApprovalCard } from "./components/ApprovalCard";
import { ArtifactPanel } from "./components/ArtifactPanel";
import { ChatWindow } from "./components/ChatWindow";
import { Composer } from "./components/Composer";
import { ContextPopover, ContextRingButton } from "./components/ContextControl";
import { ControlPanel, type ControlTab } from "./components/ControlPanel";
import { EffortButton, EffortPopover, effortLevelsOf } from "./components/EffortControl";
import { FileDrawer } from "./components/FileDrawer";
import { ForkDialog } from "./components/ForkDialog";
import { ModelButton, ModelPopover } from "./components/ModelControl";
import { PermissionButton, PermissionPopover } from "./components/PermissionControl";
import { ProjectDialog, type FolderPickResult, type ProjectDraft } from "./components/ProjectDialog";
import { SessionRail } from "./components/SessionRail";
import { SettingsNav } from "./components/SettingsNav";
import { SidePanel } from "./components/SidePanel";
import { TerminalPanel } from "./components/TerminalPanel";
import { TopBar, SidebarToggleIcon } from "./components/TopBar";
import { TrajectoryView } from "./components/TrajectoryView";
import { HomeUsage } from "./components/UsagePanel";
import * as commands from "./lib/commands";
import * as fold from "./lib/fold";
import type { Block } from "./lib/fold";
import type { PathCompletionScope } from "./lib/pathCompletion";
import { useColumnWidth } from "./lib/useColumnWidth";
import { usePanelHeight } from "./lib/usePanelHeight";
import { useStream } from "./lib/useStream";
import { useTheme } from "./lib/useTheme";

interface SideSessionState {
  open: boolean;
  inherited: number;
  running: boolean;
  turnId?: string;
  blocks: Block[];
}

const EMPTY_SIDE: SideSessionState = { open: false, inherited: 0, running: false, blocks: [] };

function sendError(cause: unknown): string {
  if (cause instanceof ApiError) {
    return `消息发送失败（HTTP ${cause.status}）。${cause.message}`;
  }
  if (cause instanceof TypeError) {
    return "消息发送失败，无法连接 Atlas 服务。请检查 Atlas 是否正在运行，再重试。";
  }
  return `消息发送失败。${cause instanceof Error ? cause.message : String(cause)}`;
}

export default function App() {
  const [facts, setFacts] = useState<State | null>(null);
  const [blocksBySession, setBlocksBySession] = useState<Record<string, Block[]>>({});
  const [subagentsBySession, setSubagentsBySession] = useState(new Map<string, Map<string, SubagentUpdate>>());
  const [runningSessions, setRunningSessions] = useState<string[]>([]);
  const [turnIds, setTurnIds] = useState<Record<string, string | undefined>>({});
  const stopRequests = useRef(new Set<string>());
  const [stoppingTurns, setStoppingTurns] = useState<string[]>([]);
  const [connected, setConnected] = useState(false);
  const streamOpened = useRef(false);
  const [approvals, setApprovals] = useState<PendingQuestion[]>([]);
  const [groups, setGroups] = useState<ProjectGroup[]>([]);
  // The folder a conversation was just started in. A session file appears on
  // its first message, so until then neither the list nor the status line can
  // name that folder; this is the stand-in for the window in between.
  const [draftDir, setDraftDir] = useState("");
  const [draftDeviceId, setDraftDeviceId] = useState("");
  const [archived, setArchived] = useState(false);
  const [models, setModels] = useState<ModelList | null>(null);
  const [permission, setPermission] = useState<PermissionState | null>(null);
  const [stats, setStats] = useState<Stats | null>(null);
  const [theme, setTheme] = useTheme();
  const [controlTab, setControlTab] = useState<ControlTab | null>(null);
  const [lastSettingsTab, setLastSettingsTab] = useState<ControlTab>("models");
  const [effortOpen, setEffortOpen] = useState(false);
  const [permissionOpen, setPermissionOpen] = useState(false);
  const [contextOpen, setContextOpen] = useState(false);
  const [modelOpen, setModelOpen] = useState(false);
  const [files, setFiles] = useState(false);
  const [artifactsOpen, setArtifactsOpen] = useState(false);
  const [artifactsList, setArtifactsList] = useState<ArtifactSummary[]>([]);
  const [activeArtifactId, setActiveArtifactId] = useState("");
  const [activeArtifactVersion, setActiveArtifactVersion] = useState(0);
  const [artifactDetail, setArtifactDetail] = useState<ArtifactDetail | null>(null);
  const [artifactLoading, setArtifactLoading] = useState(false);
  const [artifactError, setArtifactError] = useState("");
  const [darkTheme, setDarkTheme] = useState(
    () => typeof document !== "undefined" && document.documentElement.classList.contains("dark"),
  );
  const [railCollapsed, setRailCollapsed] = useState<boolean>(() => {
    try {
      return window.localStorage.getItem("atlas.rail.open") === "false";
    } catch {
      return false;
    }
  });
  const [notice, setNotice] = useState("");
  const [hasComposerDraft, setHasComposerDraft] = useState(false);
  const [workspaceChanges, setWorkspaceChanges] = useState(0);
  const effortAnchorRef = useRef<HTMLButtonElement>(null);
  const permissionAnchorRef = useRef<HTMLButtonElement>(null);
  const contextAnchorRef = useRef<HTMLButtonElement>(null);
  const modelAnchorRef = useRef<HTMLButtonElement>(null);
  const [forkPoints, setForkPoints] = useState<ForkPoint[]>([]);
  const [forkingEntryId, setForkingEntryId] = useState<string | null>(null);
  const [forkDraft, setForkDraft] = useState<{ entryId: string; index?: number } | null>(null);
  const [forkError, setForkError] = useState("");
  const [projectDialogOpen, setProjectDialogOpen] = useState(false);
  const [projectSubmitting, setProjectSubmitting] = useState(false);
  const [projectError, setProjectError] = useState("");
  const [trajectory, setTrajectory] = useState<TrajectoryRow[]>([]);
  const [trajectoryOpen, setTrajectoryOpen] = useState(false);
  const [trajectoryLoading, setTrajectoryLoading] = useState(false);
  const [trajectoryError, setTrajectoryError] = useState("");
  const [terminalOpen, setTerminalOpen] = useState(false);
  const [sideBySession, setSideBySession] = useState<Record<string, SideSessionState>>({});

  // 三根列的宽度。左栏、侧对话面板与右侧文件树都能拖，拖出来的值由钩子自己记在
  // localStorage 里，这里只把它交给网格的列定义。
  const rail = useColumnWidth({
    storageKey: "atlas.rail.width",
    label: "对话列表宽度",
    initial: 264,
    min: 200,
    max: 420,
    edge: "right",
  });
  const side = useColumnWidth({
    storageKey: "atlas.side.width",
    label: "侧对话面板宽度",
    initial: 360,
    min: 280,
    max: 720,
    edge: "left",
  });
  const drawer = useColumnWidth({
    storageKey: "atlas.drawer.width",
    label: "文件树宽度",
    initial: 320,
    min: 240,
    max: 640,
    edge: "left",
  });
  const artifactDrawer = useColumnWidth({
    storageKey: "atlas.artifact.width",
    label: "产物面板宽度",
    initial: 480,
    min: 320,
    max: 900,
    edge: "left",
  });
  const terminal = usePanelHeight({
    storageKey: "atlas.terminal.height",
    label: "终端面板高度",
    initial: 250,
    min: 140,
    max: 560,
  });

  const sessions = useMemo(() => groups.flatMap((group) => group.sessions), [groups]);

  const active = useMemo(() => sessions.find((session) => session.current), [sessions]);
  const currentPath = active?.path;

  const activeSessionKey = facts?.session || currentPath || "__default__";
  const activeKeyRef = useRef(activeSessionKey);
  activeKeyRef.current = activeSessionKey;

  const activeSessionId = active?.path === activeSessionKey ? active.id : undefined;
  const sessionBlocks = blocksBySession[activeSessionKey];
  const sessionTasks = subagentsBySession.get(activeSessionKey);
  const sessionIdTasks = activeSessionId ? subagentsBySession.get(activeSessionId) : undefined;
  const blocks = useMemo(
    () => fold.withSubagents(sessionBlocks ?? [], sessionTasks, sessionIdTasks),
    [sessionBlocks, sessionTasks, sessionIdTasks],
  );
  const running = runningSessions.includes(activeSessionKey);
  const currentSide = sideBySession[activeSessionKey] ?? EMPTY_SIDE;
  const sideOpen = currentSide.open;
  const sideBlocks = currentSide.blocks;
  const sideRunning = currentSide.running;
  const sideInherited = currentSide.inherited;
  const mainTurnId = running ? turnIds[activeSessionKey] : undefined;
  const sideTurnId = sideRunning ? currentSide.turnId : undefined;

  const clearStopRequest = useCallback((turnId?: string) => {
    if (turnId && stopRequests.current.delete(turnId)) {
      setStoppingTurns((previous) => previous.filter((id) => id !== turnId));
    }
  }, []);

  // Per-session refs ensure burst deltas arriving before React commits are folded
  // sequentially against the right conversation without overwriting other sessions.
  const blocksRef = useRef<Record<string, Block[]>>({});
  const runningRef = useRef<string[]>([]);
  const changeRunning = useCallback((key: string, running: boolean) => {
    const previous = runningRef.current;
    if (previous.includes(key) === running) return;
    const next = running ? [...previous, key] : previous.filter((item) => item !== key);
    runningRef.current = next;
    setRunningSessions(next);
  }, []);
  // A session can start streaming before its history has been read in this window.
  const loadedSessionsRef = useRef<Set<string>>(new Set());
  const liveSessionsRef = useRef<Set<string>>(new Set());
  const transcriptLoadsRef = useRef(new Map<string, symbol>());
  const transcriptRefreshRef = useRef(new Set<string>());

  const commitFor = useCallback((sessionKey: string, next: Block[]) => {
    blocksRef.current = { ...blocksRef.current, [sessionKey]: next };
    setBlocksBySession((prev) => ({ ...prev, [sessionKey]: next }));
  }, []);

  const sideRef = useRef<Record<string, SideSessionState>>({});
  const updateSideFor = useCallback(
    (sessionKey: string, updater: (prev: SideSessionState) => SideSessionState) => {
      const prev = sideRef.current[sessionKey] ?? EMPTY_SIDE;
      const next = updater(prev);
      sideRef.current = { ...sideRef.current, [sessionKey]: next };
      setSideBySession((map) => ({ ...map, [sessionKey]: next }));
    },
    [],
  );

  const applyStateSnapshot = useCallback((state: State) => {
    setFacts(state);
    if (state.session) {
      activeKeyRef.current = state.session;
    }
    setApprovals(state.pending ?? []);
    const key = state.session || activeKeyRef.current;
    setTurnIds((ids) => ({ ...ids, [key]: state.turnId }));
    if (state.runningSessions !== undefined) {
      runningRef.current = state.runningSessions;
      setRunningSessions(state.runningSessions);
    } else {
      changeRunning(state.session || activeKeyRef.current, state.running);
    }
  }, [changeRunning]);

  // The rail is drawn per project, so one request answers "which conversations,
  // in which folder" instead of a flat list the page would have to sort itself.
  const refreshSessions = useCallback(async (showArchived = archived) => {
    try {
      const list = await api.projects(showArchived);
      const next = list.groups ?? [];
      setGroups(next);
      if (next.some((group) => group.sessions.some((session) => session.current))) {
        setDraftDir((dir) => (dir ? "" : dir));
        setDraftDeviceId((dev) => (dev ? "" : dev));
      }
    } catch {
      setGroups([]);
    }
  }, [archived]);

  // Keep archived read-only state grounded in /api/state too, so toggling the
  // rail back to active projects while viewing an archived session never makes
  // it look writable.
  const sessionArchived = Boolean(facts?.archived ?? active?.archived);

  // A fork needs a conversation to copy and a turn that is not running: the
  // branch under a running turn would be two writers on one file.
  const forkable = Boolean(active && !sessionArchived) && !running;

  const refreshModels = useCallback(async () => {
    try {
      setModels(await api.models());
    } catch {
      setModels(null);
    }
  }, []);

  const refreshPermission = useCallback(async () => {
    try {
      setPermission(await api.permission());
    } catch {
      setPermission(null);
    }
  }, []);

  const refreshStats = useCallback(async () => {
    try {
      setStats(await api.stats());
    } catch {
      setStats(null);
    }
  }, []);

  const artifactsOpenRef = useRef(artifactsOpen);
  artifactsOpenRef.current = artifactsOpen;
  const activeArtifactIdRef = useRef(activeArtifactId);
  activeArtifactIdRef.current = activeArtifactId;
  const activeArtifactVersionRef = useRef(activeArtifactVersion);
  activeArtifactVersionRef.current = activeArtifactVersion;
  const artifactLoadSeqRef = useRef(0);
  const artifactDetailSeqRef = useRef(0);

  useEffect(() => {
    const sync = () =>
      setDarkTheme(typeof document !== "undefined" && document.documentElement.classList.contains("dark"));
    sync();
    if (theme !== "system" || typeof window === "undefined") return;
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const listener = () => window.setTimeout(sync, 0);
    mq.addEventListener("change", listener);
    return () => mq.removeEventListener("change", listener);
  }, [theme]);

  const loadArtifactDetail = useCallback(async (id: string, version = 0) => {
    if (!id) {
      artifactDetailSeqRef.current += 1;
      setActiveArtifactId("");
      setActiveArtifactVersion(0);
      setArtifactDetail(null);
      setArtifactError("");
      setArtifactLoading(false);
      return;
    }
    const expectedSession = activeKeyRef.current;
    const seq = ++artifactDetailSeqRef.current;
    setActiveArtifactId(id);
    setActiveArtifactVersion(version);
    setArtifactLoading(true);
    setArtifactError("");
    try {
      const detail = await api.artifact(id, version);
      if (activeKeyRef.current !== expectedSession || artifactDetailSeqRef.current !== seq) return;
      setArtifactDetail(detail);
    } catch (cause) {
      if (activeKeyRef.current !== expectedSession || artifactDetailSeqRef.current !== seq) return;
      setArtifactDetail(null);
      setArtifactError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      if (artifactDetailSeqRef.current === seq) {
        setArtifactLoading(false);
      }
    }
  }, []);

  const refreshArtifacts = useCallback(
    async (opts?: { selectId?: string; selectVersion?: number; loadDetail?: boolean }) => {
      const expectedSession = activeKeyRef.current;
      const seq = ++artifactLoadSeqRef.current;
      try {
        const list = await api.artifacts();
        if (activeKeyRef.current !== expectedSession || artifactLoadSeqRef.current !== seq) return;
        setArtifactsList(list);
        const preferred = opts?.selectId;
        const current = activeArtifactIdRef.current;
        const nextId = preferred
          ? preferred
          : list.some((item) => item.id === current)
            ? current
            : (list[0]?.id ?? "");
        const nextVer = preferred
          ? (opts?.selectVersion ?? 0)
          : nextId === current
            ? activeArtifactVersionRef.current
            : 0;
        if (!nextId) {
          artifactDetailSeqRef.current += 1;
          setActiveArtifactId("");
          setActiveArtifactVersion(0);
          setArtifactDetail(null);
          setArtifactError("");
          return;
        }
        setActiveArtifactId(nextId);
        setActiveArtifactVersion(nextVer);
        if (opts?.loadDetail || preferred || artifactsOpenRef.current) {
          void loadArtifactDetail(nextId, nextVer);
        }
      } catch (cause) {
        if (activeKeyRef.current !== expectedSession || artifactLoadSeqRef.current !== seq) return;
        if (cause instanceof ApiError && cause.status === 501) {
          setArtifactsList([]);
          return;
        }
        setArtifactError(cause instanceof Error ? cause.message : String(cause));
      }
    },
    [loadArtifactDetail],
  );

  const openArtifact = useCallback(
    (id: string, version = 0) => {
      setFiles(false);
      setArtifactsOpen(true);
      void refreshArtifacts({ selectId: id, selectVersion: version, loadDetail: true });
    },
    [refreshArtifacts],
  );

  const toggleFiles = useCallback(() => {
    setFiles((value) => {
      const next = !value;
      if (next) setArtifactsOpen(false);
      return next;
    });
  }, []);

  const toggleArtifacts = useCallback(() => {
    setArtifactsOpen((value) => {
      const next = !value;
      if (next) {
        setFiles(false);
        void refreshArtifacts({ loadDetail: true });
      }
      return next;
    });
  }, [refreshArtifacts]);

  const deleteArtifactById = useCallback(
    async (id: string) => {
      await api.deleteArtifact(id);
      if (activeArtifactIdRef.current === id) {
        artifactDetailSeqRef.current += 1;
        setActiveArtifactId("");
        setActiveArtifactVersion(0);
        setArtifactDetail(null);
      }
      await refreshArtifacts({ loadDetail: true });
    },
    [refreshArtifacts],
  );

  const exportArtifactById = useCallback(
    async (id: string, version: number, relPath: string) => api.exportArtifact(id, version, relPath),
    [],
  );

  const syncConfig = useCallback(async () => {
    await Promise.all([
      refreshModels(),
      refreshPermission(),
      refreshStats(),
      api.state().then(applyStateSnapshot).catch(() => undefined),
    ]);
  }, [refreshModels, refreshPermission, refreshStats, applyStateSnapshot]);

  const applyThinking = useCallback(
    async (level: string) => {
      await api.setThinking(level);
      await refreshModels();
    },
    [refreshModels],
  );

  const applyModel = useCallback(
    async (provider: string, id: string) => {
      await api.setModel(provider, id);
      await Promise.all([
        refreshModels(),
        refreshStats(),
        api.state().then(applyStateSnapshot).catch(() => undefined),
      ]);
    },
    [refreshModels, refreshStats, applyStateSnapshot],
  );

  const applyPermission = useCallback(async (mode: string) => {
    setPermission(await api.setPermission(mode));
  }, []);

  const loadTranscriptFor = useCallback(
    async (sessionKey: string) => {
      const load = Symbol();
      transcriptLoadsRef.current.set(sessionKey, load);
      if (runningRef.current.includes(sessionKey)) transcriptRefreshRef.current.add(sessionKey);
      try {
        const items = await api.transcript();
        if (activeKeyRef.current !== sessionKey || transcriptLoadsRef.current.get(sessionKey) !== load) return;
        loadedSessionsRef.current.add(sessionKey);
        commitFor(sessionKey, fold.replay(items));
      } catch {
        // Keep any streamed blocks available while a transcript can be retried.
      } finally {
        if (transcriptLoadsRef.current.get(sessionKey) === load) transcriptLoadsRef.current.delete(sessionKey);
      }
    },
    [commitFor],
  );

  const loadSideFor = useCallback(
    async (sessionKey: string) => {
      try {
        const view = await api.side();
        updateSideFor(sessionKey, () => ({
          open: view.open,
          inherited: view.inherited,
          running: view.running,
          turnId: view.turnId,
          blocks: fold.replay(view.blocks),
        }));
      } catch {
        updateSideFor(sessionKey, () => EMPTY_SIDE);
      }
    },
    [updateSideFor],
  );

  useEffect(() => {
    let live = true;
    api
      .state()
      .then((state) => {
        if (!live) return;
        applyStateSnapshot(state);
        const key = state.session || "__default__";
        void loadTranscriptFor(key);
        void loadSideFor(key);
        void refreshArtifacts();
      })
      .catch(() => setNotice("无法读取服务状态"));
    void refreshSessions();
    void refreshModels();
    void refreshPermission();
    void refreshStats();
    return () => {
      live = false;
    };
    // Mount-only: /api/state is the snapshot a reload starts from.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useStream({
    onHello: (hello) => {
      setFacts((state) => ({ ...(state ?? { running: false }), ...hello }));
      if (hello.session && activeKeyRef.current === "__default__") {
        activeKeyRef.current = hello.session;
      }
    },
    onConnect: (online) => {
      setConnected(online);
      if (!online) return;
      if (!streamOpened.current) {
        streamOpened.current = true;
        return;
      }
      const key = activeKeyRef.current;
      void api.state().then((state) => {
        if (key !== activeKeyRef.current || key !== (state.session || "__default__")) return;
        applyStateSnapshot(state);
        if (!state.running) {
          clearStopRequest(turnIds[key]);
          void loadTranscriptFor(key);
        }
        void loadSideFor(key);
        void refreshArtifacts({ loadDetail: artifactsOpenRef.current });
      }).catch(() => undefined);
    },
    onStatus: (status) => {
      const targetKey = status.session || activeKeyRef.current;
      const wasRunning = runningRef.current.includes(targetKey);
      changeRunning(targetKey, status.running);
      setTurnIds((ids) => ({ ...ids, [targetKey]: status.running ? status.turnId : undefined }));
      if (!status.running) clearStopRequest(status.turnId);
      if (status.running) {
        liveSessionsRef.current.add(targetKey);
        if (transcriptLoadsRef.current.has(targetKey)) transcriptRefreshRef.current.add(targetKey);
      }
      if (status.aborted || status.error) {
        liveSessionsRef.current.add(targetKey);
        const existing = blocksRef.current[targetKey] ?? [];
        commitFor(targetKey, status.aborted
          ? fold.interrupt(existing)
          : fold.notice(existing, "error", status.error ?? ""));
      }
      if (!status.running && (status.aborted || wasRunning || status.receipt?.length || status.endedAt)) {
        liveSessionsRef.current.add(targetKey);
        commitFor(
          targetKey,
          fold.finishTurn(fold.settle(blocksRef.current[targetKey] ?? []), {
            endedAt: status.endedAt,
            durationMs: status.durationMs,
            tokensPerSec: status.tokensPerSec,
          }),
        );
        void refreshSessions();
        if (transcriptRefreshRef.current.delete(targetKey)) {
          // A snapshot taken mid-turn can overlap or miss streamed blocks.
          transcriptLoadsRef.current.delete(targetKey);
          loadedSessionsRef.current.delete(targetKey);
          if (targetKey === activeKeyRef.current) void loadTranscriptFor(targetKey);
        }
        if (targetKey === activeKeyRef.current) {
          void refreshModels();
          void refreshStats();
          void refreshArtifacts({ loadDetail: artifactsOpenRef.current });
        }
      }
    },
    onAgent: (event) => {
      if (event.type === "subagent_update") {
        const task = event.subagent;
        const key = event.session || task?.sessionFile || task?.sessionId;
        const callId = task?.toolCallId;
        if (!key || !task?.id || !callId) return;
        setSubagentsBySession((sessions) => {
          const tasks = sessions.get(key);
          const previous = tasks?.get(callId);
          if (previous?.id === task.id && !fold.subagentPending(previous) && fold.subagentPending(task)) {
            return sessions;
          }
          const next = new Map(tasks);
          next.set(callId, previous?.id === task.id ? { ...previous, ...task } : task);
          return new Map(sessions).set(key, next);
        });
        return;
      }
      const targetKey = event.session || activeKeyRef.current;
      liveSessionsRef.current.add(targetKey);
      const existing = blocksRef.current[targetKey] ?? [];
      commitFor(targetKey, fold.apply(existing, event).blocks);
      if (event.type === "agent_start" && targetKey === activeKeyRef.current) {
        void refreshModels();
      }
      if (
        event.type === "tool_execution_end" &&
        !event.isError &&
        !event.aborted &&
        fold.isArtifactProducerTool(event.toolName) &&
        targetKey === activeKeyRef.current
      ) {
        const details = (event.result as { details?: { id?: string; version?: number } } | undefined)?.details;
        const artId = typeof details?.id === "string" ? details.id : "";
        const artVer = typeof details?.version === "number" ? details.version : 0;
        if (artId) {
          setFiles(false);
          setArtifactsOpen(true);
          void refreshArtifacts({ selectId: artId, selectVersion: artVer, loadDetail: true });
        } else {
          void refreshArtifacts({ loadDetail: artifactsOpenRef.current });
        }
      }
    },
    onApproval: (question) => {
      setApprovals((list) => (list.some((item) => item.id === question.id) ? list : [...list, question]));
    },
    onApprovalResolved: (id) => {
      setApprovals((list) => list.filter((item) => item.id !== id));
    },
    onSideAgent: (event) => {
      const targetKey = event.session || activeKeyRef.current;
      updateSideFor(targetKey, (prev) => ({
        ...prev,
        blocks: fold.apply(prev.blocks, event).blocks,
      }));
    },
    onSideStatus: (status) => {
      const targetKey = status.session || activeKeyRef.current;
      if (!status.running) clearStopRequest(status.turnId);
      updateSideFor(targetKey, (prev) => {
        let nextBlocks = prev.blocks;
        if (status.aborted) {
          nextBlocks = fold.interrupt(nextBlocks);
        } else if (status.error) {
          nextBlocks = fold.notice(nextBlocks, "error", status.error);
        }
        if (!status.running) {
          nextBlocks = fold.finishTurn(fold.settle(nextBlocks), {
            endedAt: status.endedAt,
            durationMs: status.durationMs,
            tokensPerSec: status.tokensPerSec,
          });
        }
        return {
          ...prev,
          open: status.open,
          running: status.running,
          turnId: status.running ? status.turnId : undefined,
          blocks: nextBlocks,
        };
      });
    },
  });

  const stopTurn = useCallback(async (key: string, turnId: string, side = false) => {
    if (stopRequests.current.has(turnId)) return;
    stopRequests.current.add(turnId);
    setStoppingTurns((previous) => [...previous, turnId]);
    try {
      await api.stop(turnId, key === "__default__" ? undefined : key, side);
    } catch (cause) {
      clearStopRequest(turnId);
      if (cause instanceof ApiError && cause.status === 409) {
        if (activeKeyRef.current !== key) return;
        if (side) await loadSideFor(key);
        else await api.state().then(applyStateSnapshot).catch(() => undefined);
        return;
      }
      const text = `停止本轮失败。${cause instanceof Error ? cause.message : String(cause)}`;
      if (side) {
        updateSideFor(key, (previous) => ({ ...previous, blocks: fold.notice(previous.blocks, "error", text) }));
      } else {
        commitFor(key, fold.notice(blocksRef.current[key] ?? [], "error", text));
      }
    }
  }, [applyStateSnapshot, clearStopRequest, commitFor, loadSideFor, updateSideFor]);

  useEffect(() => {
    if ((!mainTurnId && !sideTurnId) || controlTab || forkDraft || projectDialogOpen ||
        modelOpen || effortOpen || permissionOpen || contextOpen) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.defaultPrevented || event.isComposing || event.repeat) return;
      if (document.querySelector('[aria-modal="true"]')) return;
      const target = event.target;
      const side = target instanceof Element && Boolean(target.closest(".side"));
      if (!side && target instanceof Element && target !== document.body && !target.closest(".chat")) return;
      const turnId = side ? sideTurnId : mainTurnId;
      if (!turnId) return;
      event.preventDefault();
      void stopTurn(activeSessionKey, turnId, side);
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [activeSessionKey, contextOpen, controlTab, effortOpen, forkDraft, mainTurnId, modelOpen,
    permissionOpen, projectDialogOpen, sideTurnId, stopTurn]);

  const runCommand = async (name: string, argument: string) => {
    const key = activeKeyRef.current;
    const known = commands.find(name);
    if (!known) {
      commitFor(
        key,
        fold.notice(blocksRef.current[key] ?? [], "notice", `窗口没有 ${name} 这个命令。\n\n${commands.helpText()}`),
      );
      return;
    }
    if (argument && !known.usage) {
      commitFor(key, fold.notice(blocksRef.current[key] ?? [], "notice", `${name} 不带参数`));
      return;
    }
    if (
      sessionArchived &&
      known.name !== "/help" &&
      known.name !== "/model" &&
      known.name !== "/permission" &&
      known.name !== "/mcp" &&
      known.name !== "/plugin" &&
      known.name !== "/browser" &&
      !(known.name === "/skill" && !argument.trim())
    ) {
      return;
    }
    switch (known.name) {
      case "/help":
        commitFor(key, fold.notice(blocksRef.current[key] ?? [], "notice", commands.helpText()));
        return;
      case "/compact":
        if (running) {
          commitFor(key, fold.notice(blocksRef.current[key] ?? [], "notice", "这一轮还在跑，等它结束再压缩"));
          return;
        }
        changeRunning(key, true);
        try {
          await api.compact(argument, facts?.session);
          void refreshStats();
        } catch (cause) {
          if (cause instanceof ApiError && cause.status === 409) {
            commitFor(key, fold.notice(blocksRef.current[key] ?? [], "notice", "这一轮还在跑，等它结束再压缩"));
            return;
          }
          commitFor(
            key,
            fold.notice(blocksRef.current[key] ?? [], "error", cause instanceof Error ? cause.message : String(cause)),
          );
        } finally {
          changeRunning(key, false);
        }
        return;
      case "/side":
        if (argument === "off") {
          await closeSide();
          return;
        }
        if (argument) {
          commitFor(key, fold.notice(blocksRef.current[key] ?? [], "notice", "/side 只认 off：/side 开，/side off 关"));
          return;
        }
        await openSide();
        return;
      case "/btw":
        if (argument === "off") {
          await closeSide();
          return;
        }
        if (argument) {
          await askSide(argument);
          return;
        }
        await openSide();
        return;
      case "/model":
        setEffortOpen(false);
        setPermissionOpen(false);
        setContextOpen(false);
        setModelOpen(true);
        return;
      case "/permission":
        if (permission) {
          setEffortOpen(false);
          setContextOpen(false);
          setModelOpen(false);
          setPermissionOpen(true);
        } else {
          setEffortOpen(false);
          setContextOpen(false);
          setModelOpen(false);
          openControlTab("general");
        }
        return;
      case "/mcp":
        setEffortOpen(false);
        setPermissionOpen(false);
        openControlTab("mcp");
        return;
      case "/plugin":
        setEffortOpen(false);
        setPermissionOpen(false);
        openControlTab("plugins");
        return;
      case "/remote":
        setEffortOpen(false);
        setPermissionOpen(false);
        openControlTab("remote");
        return;
      case "/schedule":
        setEffortOpen(false);
        setPermissionOpen(false);
        openControlTab("schedules");
        return;
      case "/browser": {
        setEffortOpen(false);
        setPermissionOpen(false);
        const target = argument.trim();
        if (target) {
          try {
            await api.browserOpen(target);
            window.dispatchEvent(new CustomEvent("atlas:browser-opened"));
          } catch (cause) {
            setNotice(cause instanceof Error ? cause.message : String(cause));
          }
        }
        openControlTab("browser");
        return;
      }
      case "/skill": {
        const trimmed = argument.trim();
        if (!trimmed) {
          setEffortOpen(false);
          setPermissionOpen(false);
          openControlTab("skills");
          return;
        }
        if (running) {
          setNotice("这一轮还在跑，等它结束再加载 skill");
          return;
        }
        const [skillName, ...rest] = trimmed.split(/\s+/);
        try {
          const { prompt } = await api.loadSkillPrompt(skillName, rest.join(" "));
          await submitTurn(prompt);
        } catch (cause) {
          commitFor(
            key,
            fold.notice(
              blocksRef.current[key] ?? [],
              "error",
              cause instanceof Error ? cause.message : String(cause),
            ),
          );
        }
        return;
      }
      case "/fork":
        if (running) {
          setNotice("这一轮还在跑，等它结束再分叉");
          return;
        }
        if (blocks.length === 0) {
          setNotice("还没有消息，先问一句再分叉");
          return;
        }
        openForkDialog("");
        return;
    }
  };

  const submitTurn = async (text: string, attachments?: AttachmentPayload[]) => {
    if (sessionArchived) return;
    const key = activeKeyRef.current;
    liveSessionsRef.current.add(key);
    const attachmentSummary =
      attachments && attachments.length > 0
        ? attachments
            .map((item) => `[${item.kind === "image" ? "图片" : item.kind === "pdf" ? "PDF" : "文本"}: ${item.name}]`)
            .join(" ")
        : "";
    const displayPrompt = [text, attachmentSummary].filter(Boolean).join("\n");
    commitFor(key, fold.prompt(blocksRef.current[key] ?? [], displayPrompt));
    changeRunning(key, true);
    setNotice("");
    try {
      await api.ask(text, facts?.session, attachments);
    } catch (cause) {
      changeRunning(key, false);
      commitFor(key, fold.notice(blocksRef.current[key] ?? [], "error", sendError(cause)));
    }
  };

  const send = async (text: string, attachments?: AttachmentPayload[]) => {
    if (sessionArchived) return;
    const command = !attachments?.length ? commands.parse(text) : null;
    if (command) {
      await runCommand(command.name, command.argument);
      return;
    }
    await submitTurn(text, attachments);
  };

  // ---------- 侧对话 ----------

  const openSide = async () => {
    const key = activeKeyRef.current;
    try {
      const view = await api.openSide();
      updateSideFor(key, () => ({
        open: true,
        inherited: view.inherited,
        running: view.running,
        turnId: view.turnId,
        blocks: fold.replay(view.blocks),
      }));
    } catch (cause) {
      setNotice(cause instanceof Error ? cause.message : String(cause));
    }
  };

  const closeSide = async () => {
    const key = activeKeyRef.current;
    try {
      await api.closeSide();
      updateSideFor(key, () => EMPTY_SIDE);
    } catch (cause) {
      setNotice(cause instanceof Error ? cause.message : String(cause));
    }
  };

  const askSide = async (text: string) => {
    const prompt = text.trim();
    if (!prompt) return;
    const key = activeKeyRef.current;
    try {
      if (!sideRef.current[key]?.open) {
        const view = await api.openSide();
        updateSideFor(key, () => ({
          open: true,
          inherited: view.inherited,
          running: view.running,
          turnId: view.turnId,
          blocks: fold.replay(view.blocks),
        }));
      }
      updateSideFor(key, (prev) => ({
        ...prev,
        running: true,
        blocks: fold.prompt(prev.blocks, prompt),
      }));
      await api.sideAsk(prompt, facts?.session);
    } catch (cause) {
      updateSideFor(key, (prev) => ({
        ...prev,
        running: false,
        blocks: fold.notice(prev.blocks, "error", sendError(cause)),
      }));
    }
  };

  const sendSide = async (text: string) => {
    const command = commands.parse(text);
    const key = activeKeyRef.current;
    if (!command) {
      await askSide(text);
      return;
    }
    if (command.name === "/side" || command.name === "/btw") {
      if (command.argument === "off") {
        await closeSide();
        return;
      }
      if (command.name === "/btw" && command.argument) {
        await askSide(command.argument);
        return;
      }
      updateSideFor(key, (prev) => ({
        ...prev,
        blocks: fold.notice(prev.blocks, "notice", "面板已经开着。问一句，或者 /side off 关掉它"),
      }));
      return;
    }
    updateSideFor(key, (prev) => ({
      ...prev,
      blocks: fold.notice(
        prev.blocks,
        "notice",
        `${command.name} 在侧对话里不跑。回主对话的输入框敲；这里只认 /btw 和 /side off`,
      ),
    }));
  };

  // The file API follows the server's active session even before its new state
  // reaches this window. Pause completion until the active workspace is known.
  const changeWorkspace = async (change: () => Promise<unknown>) => {
    setWorkspaceChanges((pending) => pending + 1);
    try {
      await change();
      const state = await api.state();
      applyStateSnapshot(state);
      return state;
    } finally {
      setWorkspaceChanges((pending) => pending - 1);
    }
  };

  const switchSession = async (path: string) => {
    try {
      setControlTab(null);
      const state = await changeWorkspace(() => api.openSession(path));
      const targetKey = state.session || path;
      // Only reuse cached blocks when this session produced live turns/streams in
      // memory during this window session; otherwise load persisted history from disk.
      const keepLive = loadedSessionsRef.current.has(targetKey) && liveSessionsRef.current.has(targetKey);
      if (!keepLive) {
        await loadTranscriptFor(targetKey);
      }
      setNotice("");
      setForkDraft(null);
      setForkError("");
      setDraftDir("");
      setDraftDeviceId("");
      artifactDetailSeqRef.current += 1;
      setActiveArtifactId("");
      setActiveArtifactVersion(0);
      setArtifactDetail(null);
      setArtifactError("");
      await refreshSessions();
      void refreshModels();
      void refreshStats();
      void loadSideFor(targetKey);
      void refreshArtifacts({ loadDetail: artifactsOpenRef.current });
    } catch (cause) {
      setNotice(cause instanceof Error ? cause.message : String(cause));
    }
  };

  const loadTrajectory = useCallback(async () => {
    setTrajectoryLoading(true);
    try {
      setTrajectory(await api.trajectory());
      setTrajectoryError("");
    } catch (cause) {
      setTrajectory([]);
      setTrajectoryError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setTrajectoryLoading(false);
    }
  }, []);

  const newSession = async (cwd?: string, deviceId?: string) => {
    const currentDevice = facts?.deviceId || active?.deviceId || draftDeviceId || "";
    const targetDevice = deviceId !== undefined ? deviceId : cwd ? "" : currentDevice;
    const dir =
      cwd ??
      sessions.find((session) => session.current)?.cwd ??
      (draftDir || (facts?.archived ? facts.directory ?? "" : ""));
    try {
      setControlTab(null);
      const state = await changeWorkspace(() => api.newSession(cwd, targetDevice));
      const nextKey = state.session || "__default__";
      loadedSessionsRef.current.add(nextKey);
      commitFor(nextKey, []);
      setNotice("");
      setForkDraft(null);
      setForkError("");
      setDraftDir(dir);
      setDraftDeviceId(targetDevice);
      artifactDetailSeqRef.current += 1;
      setArtifactsList([]);
      setActiveArtifactId("");
      setActiveArtifactVersion(0);
      setArtifactDetail(null);
      setArtifactError("");
      setArtifactsOpen(false);
      if (archived) {
        setArchived(false);
        await refreshSessions(false);
      } else {
        await refreshSessions();
      }
      void refreshModels();
      void refreshStats();
      void loadSideFor(nextKey);
      void refreshArtifacts();
    } catch (cause) {
      setNotice(cause instanceof Error ? cause.message : String(cause));
    }
  };

  const openForkDialog = (entryID: string) => {
    const point = entryID ? forkPoints.find((item) => item.entryId === entryID) : undefined;
    setForkError("");
    setForkDraft({ entryId: entryID, index: point?.index });
  };

  const forkSession = async (entryID: string, name: string) => {
    setForkingEntryId(entryID || "end");
    setForkError("");
    try {
      const state = await changeWorkspace(() => api.forkSession(entryID, name));
      const forkedKey = state.session || "__default__";
      const replayed = fold.replay(await api.transcript());
      loadedSessionsRef.current.add(forkedKey);
      liveSessionsRef.current.add(forkedKey);
      commitFor(forkedKey, fold.notice(replayed, "notice", forkNotice(entryID, forkPoints, name)));
      setForkDraft(null);
      setDraftDir("");
      setDraftDeviceId("");
      artifactDetailSeqRef.current += 1;
      setActiveArtifactId("");
      setActiveArtifactVersion(0);
      setArtifactDetail(null);
      setArtifactError("");
      await refreshSessions();
      void refreshModels();
      void refreshStats();
      void loadSideFor(forkedKey);
      void refreshArtifacts({ loadDetail: artifactsOpenRef.current });
    } catch (cause) {
      setForkError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setForkingEntryId(null);
    }
  };

  const openProjectDialog = () => {
    setProjectError("");
    setProjectDialogOpen(true);
  };

  const pickProjectFolder = async (): Promise<FolderPickResult> => {
    try {
      const chosen = await api.pickFolder();
      return { path: chosen.path };
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 501) {
        return { unsupported: true };
      }
      throw cause;
    }
  };

  const addProject = async ({ path, name, deviceId }: ProjectDraft) => {
    setProjectSubmitting(true);
    setProjectError("");
    try {
      await api.addProject(path, name, deviceId ?? "");
      setNotice("");
      setProjectDialogOpen(false);
      await refreshSessions();
    } catch (cause) {
      setProjectError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setProjectSubmitting(false);
    }
  };

  const mutate = async (action: () => Promise<void>) => {
    try {
      await action();
      setNotice("");
      await refreshSessions();
    } catch (cause) {
      setNotice(cause instanceof Error ? cause.message : String(cause));
    }
  };

  const resetCurrentSession = (state: State, cwd?: string, retiredKey?: string) => {
    if (retiredKey) {
      loadedSessionsRef.current.delete(retiredKey);
      liveSessionsRef.current.delete(retiredKey);
      transcriptLoadsRef.current.delete(retiredKey);
      transcriptRefreshRef.current.delete(retiredKey);
      const nextBlocks = { ...blocksRef.current };
      delete nextBlocks[retiredKey];
      blocksRef.current = nextBlocks;
      setBlocksBySession(nextBlocks);
    }
    const nextKey = state.session || "__default__";
    loadedSessionsRef.current.add(nextKey);
    commitFor(nextKey, []);
    setForkDraft(null);
    setForkError("");
    setDraftDir(cwd ?? "");
    setDraftDeviceId("");
    setTrajectory([]);
    setTrajectoryError("");
    artifactDetailSeqRef.current += 1;
    setArtifactsList([]);
    setActiveArtifactId("");
    setActiveArtifactVersion(0);
    setArtifactDetail(null);
    setArtifactError("");
    setArtifactsOpen(false);
    void refreshModels();
    void loadSideFor(nextKey);
    void refreshArtifacts();
  };

  const archiveSession = async (session: SessionInfo) => {
    const wasCurrent = session.path === currentPath || Boolean(session.current);
    try {
      if (wasCurrent) {
        const state = await changeWorkspace(() => api.archiveSession(session.path));
        resetCurrentSession(state, session.cwd, session.path);
      } else {
        await api.archiveSession(session.path);
      }
      setNotice("");
      await refreshSessions();
    } catch (cause) {
      setNotice(cause instanceof Error ? cause.message : String(cause));
    }
  };

  const restoreSession = async (session: SessionInfo) => {
    try {
      await api.restoreSession(session.path);
      setNotice("");
      setFacts(await api.state());
      await refreshSessions();
    } catch (cause) {
      setNotice(cause instanceof Error ? cause.message : String(cause));
    }
  };

  const deleteSession = async (session: SessionInfo) => {
    const wasCurrent = session.path === currentPath || Boolean(session.current);
    try {
      if (wasCurrent) {
        const state = await changeWorkspace(() => api.deleteSession(session.path));
        resetCurrentSession(state, session.cwd, session.path);
      } else {
        await api.deleteSession(session.path);
      }
      setNotice("");
      await refreshSessions();
    } catch (cause) {
      setNotice(cause instanceof Error ? cause.message : String(cause));
    }
  };

  // The points are read when the conversation changes and when a turn ends,
  // because those are the two moments the list changes: a new session has its
  // own answers, and a finished turn adds one. Running queries while a turn is in
  // flight would ask about a reply that is still being written, so it waits.
  useEffect(() => {
    if (!forkable) {
      setForkPoints([]);
      return;
    }
    let current = true;
    void api
      .forkPoints()
      .then((points) => {
        if (current) setForkPoints(points);
      })
      .catch(() => {
        // The list is an affordance, not the conversation: a rail that cannot
        // say where it can branch should still draw the transcript. The fork
        // itself reports the failure when it is asked to copy.
      });
    return () => {
      current = false;
    };
  }, [forkable, currentPath, blocks.length]);

  // 轨迹只在打开时才取，并且在这两件事之后重取：换了一段会话（回答的必须是屏幕
  // 上这一段），或者一轮跑完了（记录刚写完）。关着的时候这个 effect 什么都不做，
  // 所以它不会跟着每一轮变成一次多余的请求。
  useEffect(() => {
    if (!trajectoryOpen || running) return;
    void loadTrajectory();
  }, [trajectoryOpen, running, currentPath, loadTrajectory]);

  const title = useMemo(() => {
    if (!active) return "新对话";
    const first = (active.firstMessage ?? "").replace(/\s+/g, " ").trim();
    return active.name || (first ? first.slice(0, 72) : "新对话");
  }, [active]);

  // The branch's default name counts rather than describes. One conversation can
  // be forked more than once, and a suggestion that reads the same every time is
  // exactly how the rail ends up with rows the reader cannot tell apart; the
  // number is the first one no conversation in view already answers to, so
  // forking twice gives "1 X" and "2 X". Names in the archive are not consulted:
  // they are not in this list, and a repeat there is a row in another view.
  //
  // The title is prefixed as it stands, never with its own number stripped: a
  // conversation legitimately named "2024 年度总结" must not become "年度总结".
  const forkName = useMemo(() => {
    const taken = new Set(sessions.map((session) => session.name ?? ""));
    for (let n = 1; n <= 99; n += 1) {
      const candidate = `${n} ${title}`;
      if (!taken.has(candidate)) return candidate;
    }
    return title;
  }, [sessions, title]);

  // The status line names the folder the conversation in view runs in, which is
  // the project the reader picked — not the one this process was started in. A
  // conversation that has not been written yet has no row to read it from, so
  // the folder it was started in stands in until its first message lands.
  const activeDirectory = useMemo(
    () => sessions.find((session) => session.current)?.cwd ?? "",
    [sessions],
  );

  const statusDirectory = activeDirectory || draftDir || facts?.directory || "";
  const activeDeviceId = facts?.deviceId || active?.deviceId || draftDeviceId || "";
  const fileSession = facts?.session || "__default__";
  const fileDeviceId = facts?.deviceId || "";
  const fileDirectory = facts?.directory;
  const workspaceReady = facts !== null && workspaceChanges === 0;
  const fileScope = useMemo<PathCompletionScope | undefined>(() => workspaceReady
    ? { session: fileSession, deviceId: fileDeviceId, directory: fileDirectory }
    : undefined, [workspaceReady, fileSession, fileDeviceId, fileDirectory]);
  const activeDeviceName = useMemo(() => {
    if (!activeDeviceId) return "";
    const match = groups.find((g) => g.deviceId === activeDeviceId && g.deviceName);
    return match?.deviceName || activeDeviceId;
  }, [groups, activeDeviceId]);

  useEffect(() => {
    if (effortLevelsOf(models).length === 0 || running || sessionArchived) {
      setEffortOpen(false);
    }
  }, [models, running, sessionArchived]);

  const modelLabel = models?.current ?? facts?.model ?? "";

  const setRailOpen = useCallback((open: boolean) => {
    setRailCollapsed(!open);
    try {
      window.localStorage.setItem("atlas.rail.open", open ? "true" : "false");
    } catch {
      // Ignore storage errors (private mode / quota).
    }
  }, []);

  const openControlTab = (target: ControlTab | null) => {
    setEffortOpen(false);
    setPermissionOpen(false);
    setContextOpen(false);
    setModelOpen(false);
    if (target) {
      setRailOpen(true);
      setLastSettingsTab(target);
      void refreshStats();
    }
    setControlTab(target);
  };

  const toggleSettings = () => {
    setEffortOpen(false);
    setPermissionOpen(false);
    setContextOpen(false);
    setModelOpen(false);
    setControlTab((prev) => {
      if (prev !== null) return null;
      setRailOpen(true);
      void refreshStats();
      return lastSettingsTab;
    });
  };

  const visibleApprovals = approvals.filter(
    (question) => !question.session || question.session === activeSessionKey,
  );
  const approvalPaths = Array.from(
    new Set(approvals.map((question) => question.session).filter((s): s is string => Boolean(s))),
  );

  // 还没有消息时这一列还没有内容：输入框因此居中，像打开一张白纸；写了第一句之后它
  // 回到底部，因为那时的重心是上面那段对话。轨迹是一个模式，它占着阅读列时不居中。
  const empty = blocks.length === 0 && !trajectoryOpen;
  const showHomeUsage = empty && facts !== null && !running && !sessionArchived && controlTab === null &&
    (active?.path !== activeSessionKey || active.messages === 0);

  // 两根列的宽度走 CSS 变量而不是内联的 grid-template-columns：列数随面板开合变
  // （一列、两列、三列），把整条 grid-template-columns 从页面拼出来等于把 panels.css
  // 里那三条规则抄一遍，而抄出来的第二份迟早与第一份不一样。
  const shellStyle = {
    "--rail-width": `${rail.width}px`,
    "--side-width": `${side.width}px`,
    "--drawer-width": `${artifactsOpen ? artifactDrawer.width : drawer.width}px`,
  } as CSSProperties;

  return (
    <div
      className="app"
      style={shellStyle}
      data-rail-collapsed={railCollapsed ? "true" : undefined}
      data-resizing={
        rail.dragging || side.dragging || drawer.dragging || artifactDrawer.dragging || terminal.dragging
          ? "true"
          : undefined
      }
    >
      <nav
        className="rail"
        data-settings={controlTab !== null ? "true" : undefined}
        aria-hidden={railCollapsed || undefined}
      >
        <div className="rail__surface">
          {controlTab !== null ? (
            <SettingsNav
              active={controlTab}
              mcpCount={stats?.mcpServers}
              skillCount={stats?.skills}
              onSelect={openControlTab}
              onBack={() => setControlTab(null)}
            />
          ) : (
            <>
              <div className="rail__head">
                <span className="rail__spacer" />
                <button
                  type="button"
                  className="icon-button rail__collapse-btn"
                  onClick={() => setRailOpen(false)}
                  title="收起侧边栏"
                  aria-label="收起侧边栏"
                >
                  <SidebarToggleIcon collapsed={false} />
                </button>
              </div>

              <SessionRail
                groups={groups}
                archived={archived}
                current={currentPath}
                pendingDir={currentPath || sessionArchived ? "" : draftDir}
                pendingKey={
                  currentPath || sessionArchived || !draftDir
                    ? ""
                    : `${draftDeviceId || "local"}:${draftDir}`
                }
                runningPaths={runningSessions}
                approvalPaths={approvalPaths}
                onToggleArchived={() => {
                  const next = !archived;
                  setArchived(next);
                  void refreshSessions(next);
                }}
                onOpen={(session) => void switchSession(session.path)}
                onNew={() => void newSession()}
                onNewIn={(cwd, deviceId) => void newSession(cwd, deviceId)}
                onAddProject={openProjectDialog}
                onRemoveProject={(path, deviceId) =>
                  void mutate(() => api.removeProject(path, deviceId ?? ""))
                }
                onConnectDevice={async (deviceId) => {
                  await mutate(async () => {
                    await api.connectDevice(deviceId);
                  });
                }}
                onArchive={(session) => void archiveSession(session)}
                onRestore={(session) => void restoreSession(session)}
                onDelete={(session) => void deleteSession(session)}
              />
            </>
          )}

          <div className="rail__foot">
            <button
              type="button"
              className="rail__settings-btn"
              data-active={controlTab !== null ? "true" : undefined}
              aria-pressed={controlTab !== null}
              onClick={toggleSettings}
              aria-label="设置"
              title={controlTab !== null ? "关闭设置并返回对话" : "打开设置"}
            >
              <span className="rail__settings-glyph" aria-hidden="true">
                ⚙
              </span>
              <span>{controlTab !== null ? "返回对话" : "设置"}</span>
            </button>
            <span className="rail__version">{facts?.version ? `v${facts.version}` : ""}</span>
          </div>
        </div>

        {/* 把手是最后一根子元素而不是列表里的行：它盖在边线上，不占列表的位置。 */}
        <div className="resizer resizer--right" title="拖动改变侧边栏宽度，双击复位" {...rail.handleProps} />
      </nav>

      <main className="main">
        <div
          className="workspace"
          data-drawer={files || artifactsOpen ? "open" : "closed"}
          data-side={sideOpen ? "open" : "closed"}
        >
          <div className="center-pane">
            <TopBar
              title={title}
              directory={statusDirectory}
              deviceName={activeDeviceName || undefined}
              connected={connected}
              running={running}
              archived={sessionArchived}
              railCollapsed={railCollapsed}
              filesOpen={files}
              artifactsOpen={artifactsOpen}
              artifactCount={artifactsList.length}
              terminalOpen={terminalOpen}
              trajectoryOpen={trajectoryOpen}
              onExpandRail={() => setRailOpen(true)}
              onToggleTerminal={() => setTerminalOpen((value) => !value)}
              onToggleFiles={toggleFiles}
              onToggleArtifacts={toggleArtifacts}
              onToggleTrajectory={() => setTrajectoryOpen((value) => !value)}
            />

            <div className="chat" data-empty={empty ? "true" : "false"}>
            {trajectoryOpen ? (
              <TrajectoryView
                rows={trajectory}
                loading={trajectoryLoading}
                error={trajectoryError}
                onRefresh={() => void loadTrajectory()}
                onClose={() => setTrajectoryOpen(false)}
              />
            ) : (
              <ChatWindow
                blocks={blocks}
                running={running}
                forkPoints={forkable ? forkPoints : undefined}
                onFork={forkable ? (entryID) => openForkDialog(entryID) : undefined}
                forkingEntryId={forkingEntryId}
                activeArtifactId={artifactsOpen ? activeArtifactId : undefined}
                onOpenArtifact={openArtifact}
              />
            )}

            {visibleApprovals.length > 0 ? (
              <div className="approvals">
                {visibleApprovals.map((question) => (
                  <ApprovalCard
                    key={question.id}
                    question={question}
                    onSettled={(id) =>
                      setApprovals((list) => list.filter((item) => item.id !== id))
                    }
                  />
                ))}
              </div>
            ) : null}

            {notice ? <div className="approvals notice notice--error">{notice}</div> : null}

            <Composer
              disabled={running}
              stopping={Boolean(mainTurnId && stoppingTurns.includes(mainTurnId))}
              onStop={mainTurnId ? () => void stopTurn(activeSessionKey, mainTurnId) : undefined}
              locked={sessionArchived}
              allowAttachments
              fileScope={fileScope}
              placeholder={sessionArchived ? "此会话已归档，只能查看、恢复或删除，无法继续发消息" : undefined}
              onSend={(text, attachments) => void send(text, attachments)}
              onDraftChange={setHasComposerDraft}
              footer={
                <>
                  <ModelButton
                    label={modelLabel}
                    pending={models?.pending}
                    open={modelOpen}
                    onToggle={() => {
                      setEffortOpen(false);
                      setPermissionOpen(false);
                      setContextOpen(false);
                      setModelOpen((value) => !value);
                    }}
                    buttonRef={modelAnchorRef}
                  />
                  <PermissionButton
                    permission={permission}
                    open={permissionOpen}
                    onToggle={() => {
                      setEffortOpen(false);
                      setContextOpen(false);
                      setModelOpen(false);
                      setPermissionOpen((value) => !value);
                    }}
                    buttonRef={permissionAnchorRef}
                  />
                </>
              }
              footerRight={
                <>
                  <ContextRingButton
                    stats={stats}
                    open={contextOpen}
                    onToggle={() => {
                      setEffortOpen(false);
                      setPermissionOpen(false);
                      setModelOpen(false);
                      const next = !contextOpen;
                      setContextOpen(next);
                      if (next) {
                        void refreshStats();
                      }
                    }}
                    buttonRef={contextAnchorRef}
                  />
                  <EffortButton
                    models={models}
                    running={running}
                    archived={sessionArchived}
                    open={effortOpen}
                    onToggle={() => {
                      setPermissionOpen(false);
                      setContextOpen(false);
                      setModelOpen(false);
                      setEffortOpen((value) => !value);
                    }}
                    buttonRef={effortAnchorRef}
                  />
                </>
              }
              footerOverlay={
                <>
                  <ModelPopover
                    open={modelOpen}
                    models={models}
                    anchorRef={modelAnchorRef}
                    onSelect={applyModel}
                    onClose={() => setModelOpen(false)}
                  />
                  <PermissionPopover
                    open={permissionOpen}
                    permission={permission}
                    anchorRef={permissionAnchorRef}
                    onSelect={applyPermission}
                    onClose={() => setPermissionOpen(false)}
                  />
                  <ContextPopover
                    open={contextOpen}
                    stats={stats}
                    anchorRef={contextAnchorRef}
                    onClose={() => setContextOpen(false)}
                  />
                  <EffortPopover
                    open={effortOpen}
                    models={models}
                    disabled={running || sessionArchived}
                    anchorRef={effortAnchorRef}
                    onSelect={applyThinking}
                    onClose={() => setEffortOpen(false)}
                  />
                </>
              }
            />

            {showHomeUsage ? <HomeUsage hidden={hasComposerDraft} /> : null}
          </div>

          <TerminalPanel
            open={terminalOpen}
            directory={statusDirectory}
            deviceId={activeDeviceId}
            height={terminal.height}
            resizing={terminal.dragging}
            resizer={terminal.handleProps}
            onClose={() => setTerminalOpen(false)}
          />
          </div>

          {sideOpen ? (
            <SidePanel
              blocks={sideBlocks}
              inherited={sideInherited}
              running={sideRunning}
              fileScope={fileScope}
              stopping={Boolean(sideTurnId && stoppingTurns.includes(sideTurnId))}
              onStop={sideTurnId ? () => void stopTurn(activeSessionKey, sideTurnId, true) : undefined}
              onAsk={(text) => void sendSide(text)}
              onClose={() => void closeSide()}
              resizer={side.handleProps}
            />
          ) : null}

          {artifactsOpen ? (
            <ArtifactPanel
              open={artifactsOpen}
              sessionKey={activeSessionKey}
              artifacts={artifactsList}
              selectedId={activeArtifactId}
              selectedVersion={activeArtifactVersion}
              detail={artifactDetail}
              loading={artifactLoading}
              error={artifactError}
              dark={darkTheme}
              hasWorkspace={Boolean(statusDirectory)}
              resizer={artifactDrawer.handleProps}
              onSelectArtifact={(id, version = 0) => void loadArtifactDetail(id, version)}
              onDeleteArtifact={deleteArtifactById}
              onExportArtifact={exportArtifactById}
              onRefresh={() => void refreshArtifacts({ loadDetail: true })}
              onClose={() => setArtifactsOpen(false)}
            />
          ) : (
            <FileDrawer
              open={files}
              sessionKey={activeSessionKey}
              rootDir={statusDirectory}
              running={running}
              resizer={drawer.handleProps}
              onClose={() => setFiles(false)}
            />
          )}
        </div>

        <ControlPanel
          tab={controlTab}
          onTabChange={openControlTab}
          theme={theme}
          onTheme={setTheme}
          canRunSkill={!running && !sessionArchived}
          onUseSkill={(prompt) => submitTurn(prompt)}
          onOpenSession={async (path) => {
            liveSessionsRef.current.delete(path);
            await switchSession(path);
          }}
          onClose={() => setControlTab(null)}
          onModelChanged={() => {
            void refreshModels();
            void refreshStats();
          }}
          onPermissionChanged={() => void refreshPermission()}
          onDevicesChanged={() => void refreshSessions()}
          onCapabilitiesChanged={() => void refreshStats()}
          onConfirmed={syncConfig}
        />
      </main>

      <ForkDialog
        open={forkDraft !== null}
        entryId={forkDraft?.entryId ?? ""}
        turnIndex={forkDraft?.index}
        defaultName={forkName}
        submitting={forkingEntryId !== null}
        error={forkError}
        onConfirm={(name) => {
          if (!forkDraft) return;
          void forkSession(forkDraft.entryId, name);
        }}
        onClose={() => {
          setForkError("");
          setForkDraft(null);
        }}
      />

      <ProjectDialog
        open={projectDialogOpen}
        submitting={projectSubmitting}
        error={projectError}
        onPickFolder={pickProjectFolder}
        onConfirm={(draft) => void addProject(draft)}
        onClose={() => {
          setProjectError("");
          setProjectDialogOpen(false);
        }}
      />

    </div>
  );
}

// forkNotice says what just happened to the conversation. Two facts matter: which
// branch the next message lands in, and — when the cut was in the middle — that
// the rest of it is gone. Without the second, a fork after an earlier answer
// looks like the window lost the second half of the conversation.
function forkNotice(entryID: string, points: ForkPoint[], name: string): string {
  const label = name.trim() ? `「${name.trim()}」` : "";
  if (!entryID) return `已分叉${label}，接下来的消息写进这一条分支`;
  const point = points.find((item) => (item.entryId ?? "") === entryID);
  const at = point ? `第 ${point.index} 条回答` : "选中的那条回答";
  return `已分叉${label}，只保留到${at}为止；接下来的消息写进这一条分支`;
}
