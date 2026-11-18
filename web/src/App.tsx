import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  api,
  ApiError,
  type ModelList,
  type PendingQuestion,
  type ProjectGroup,
  type State,
} from "./api";
import { ApprovalCard } from "./components/ApprovalCard";
import { ChatWindow } from "./components/ChatWindow";
import { Composer } from "./components/Composer";
import { FileDrawer } from "./components/FileDrawer";
import { SessionRail } from "./components/SessionRail";
import { SettingsSheet } from "./components/SettingsSheet";
import { TopBar } from "./components/TopBar";
import * as fold from "./lib/fold";
import type { Block } from "./lib/fold";
import { useStream } from "./lib/useStream";
import { useTheme } from "./lib/useTheme";

export default function App() {
  const [facts, setFacts] = useState<State | null>(null);
  const [blocks, setBlocks] = useState<Block[]>([]);
  const [running, setRunning] = useState(false);
  const [connected, setConnected] = useState(false);
  const [approvals, setApprovals] = useState<PendingQuestion[]>([]);
  const [groups, setGroups] = useState<ProjectGroup[]>([]);
  // The folder a conversation was just started in. A session file appears on
  // its first message, so until then neither the list nor the status line can
  // name that folder; this is the stand-in for the window in between.
  const [draftDir, setDraftDir] = useState("");
  const [archived, setArchived] = useState(false);
  const [models, setModels] = useState<ModelList | null>(null);
  const [theme, setTheme] = useTheme();
  const [settings, setSettings] = useState(false);
  const [files, setFiles] = useState(false);
  const [notice, setNotice] = useState("");

  // The transcript is written through a ref as well as state: a burst of deltas
  // can be dispatched before React has re-rendered, and folding each one
  // against the last committed array is what keeps the earlier deltas of the
  // burst. Every write goes through commit, so the two never disagree; writing
  // the ref during render instead would let a stale render move it backwards.
  const current = useRef<Block[]>([]);
  const commit = useCallback((next: Block[]) => {
    current.current = next;
    setBlocks(next);
  }, []);

  // The rail is drawn per project, so one request answers "which conversations,
  // in which folder" instead of a flat list the page would have to sort itself.
  // Archive mode is the same call: the server drops the groups whose archive is
  // empty, and a header over no rows is what an empty list looks like.
  const refreshSessions = useCallback(async (showArchived = archived) => {
    try {
      const list = await api.projects(showArchived);
      const next = list.groups ?? [];
      setGroups(next);
      // Once a conversation in view has a file the rail names it, so the
      // stand-in folder has done its job.
      if (next.some((group) => group.sessions.some((session) => session.current))) {
        setDraftDir((dir) => (dir ? "" : dir));
      }
    } catch {
      setGroups([]);
    }
  }, [archived]);

  const sessions = useMemo(() => groups.flatMap((group) => group.sessions), [groups]);

  const refreshModels = useCallback(async () => {
    try {
      setModels(await api.models());
    } catch {
      setModels(null);
    }
  }, []);

  useEffect(() => {
    let live = true;
    api
      .state()
      .then((state) => {
        if (!live) return;
        setFacts(state);
        setRunning(state.running);
        if (state.pending) setApprovals(state.pending);
      })
      .catch(() => setNotice("无法读取服务状态"));
    void refreshSessions();
    void refreshModels();
    void loadTranscript();
    return () => {
      live = false;
    };
    // Mount-only: /api/state is the snapshot a reload starts from.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useStream({
    onHello: (hello) => setFacts((state) => ({ ...(state ?? { running: false }), ...hello })),
    onConnect: setConnected,
    onStatus: (status) => {
      setRunning(status.running);
      if (status.error) {
        commit(fold.notice(current.current, "error", status.error));
      }
      if (!status.running) {
        commit(fold.settle(current.current));
        void refreshSessions();
      }
    },
    onAgent: (event) => {
      // The delta that opened this block may have arrived in the same burst as
      // the one being folded now, so the fold starts from what was committed
      // rather than from what was last rendered.
      commit(fold.apply(current.current, event).blocks);
    },
    onApproval: (question) => {
      setApprovals((list) => (list.some((item) => item.id === question.id) ? list : [...list, question]));
    },
  });

  const send = async (text: string) => {
    commit(fold.prompt(current.current, text));
    setRunning(true);
    setNotice("");
    try {
      await api.ask(text);
    } catch (cause) {
      setRunning(false);
      setNotice(cause instanceof Error ? cause.message : String(cause));
    }
  };

  const switchSession = async (path: string) => {
    try {
      await api.openSession(path);
      // The switch only moves the kernel; what the conversation holds comes
      // from a second question. Without it the page went blank, which reads as
      // "the history is gone" rather than "it has not been asked for yet".
      commit(fold.replay(await api.transcript()));
      setDraftDir("");
      await refreshSessions();
      setFacts(await api.state());
    } catch (cause) {
      setNotice(cause instanceof Error ? cause.message : String(cause));
    }
  };

  // The conversation the process was started with already has a file when the
  // window is reopened, so the first paint reads it back too.
  const loadTranscript = useCallback(async () => {
    try {
      commit(fold.replay(await api.transcript()));
    } catch {
      commit([]);
    }
  }, [commit]);

  // A conversation starts where the last one ran unless a project's own button
  // named a folder; either way the page then re-reads the rail, because a new
  // conversation may be the first one in that folder.
  const newSession = async (cwd?: string) => {
    const dir = cwd ?? sessions.find((session) => session.current)?.cwd ?? draftDir;
    try {
      await api.newSession(cwd);
      commit([]);
      setDraftDir(dir);
      await refreshSessions();
      setFacts(await api.state());
    } catch (cause) {
      setNotice(cause instanceof Error ? cause.message : String(cause));
    }
  };

  // False means there is no folder chooser to open (a browser has none), which
  // is the rail's cue to ask for a path instead.
  const pickProject = async (): Promise<boolean> => {
    try {
      const chosen = await api.pickFolder();
      if (chosen.path) {
        await api.addProject(chosen.path);
        await refreshSessions();
      }
      return true;
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 501) return false;
      setNotice(cause instanceof Error ? cause.message : String(cause));
      return true;
    }
  };

  const mutate = async (action: () => Promise<void>) => {
    try {
      await action();
      await refreshSessions();
    } catch (cause) {
      setNotice(cause instanceof Error ? cause.message : String(cause));
    }
  };

  const currentPath = useMemo(
    () => sessions.find((session) => session.current)?.path,
    [sessions],
  );

  const title = useMemo(() => {
    const active = sessions.find((session) => session.current);
    if (!active) return "atlas";
    const first = (active.firstMessage ?? "").replace(/\s+/g, " ").trim();
    return active.name || (first ? first.slice(0, 72) : "新对话");
  }, [sessions]);

  // The status line names the folder the conversation in view runs in, which is
  // the project the reader picked — not the one this process was started in. A
  // conversation that has not been written yet has no row to read it from, so
  // the folder it was started in stands in until its first message lands.
  const activeDirectory = useMemo(
    () => sessions.find((session) => session.current)?.cwd ?? "",
    [sessions],
  );

  const statusDirectory = activeDirectory || draftDir || facts?.directory || "";

  const modelLabel = models?.current ?? facts?.model ?? "";

  return (
    <div className="app">
      <nav className="rail">
        <div className="rail__head">
          <span className="rail__brand">
            <span className="rail__mark">A</span>
            atlas
          </span>
          <span className="rail__spacer" />
          <button
            type="button"
            className="icon-button"
            onClick={() => setSettings(true)}
            aria-label="设置"
            title="设置"
          >
            ⚙
          </button>
        </div>

        <SessionRail
          groups={groups}
          archived={archived}
          current={currentPath}
          pendingDir={currentPath ? "" : draftDir}
          onToggleArchived={() => {
            const next = !archived;
            setArchived(next);
            void refreshSessions(next);
          }}
          onOpen={(session) => void switchSession(session.path)}
          onNew={() => void newSession()}
          onNewIn={(cwd) => void newSession(cwd)}
          onPickProject={pickProject}
          onAddProjectPath={(path) => void mutate(() => api.addProject(path))}
          onRemoveProject={(path) => void mutate(() => api.removeProject(path))}
          onArchive={(session) => void mutate(() => api.archiveSession(session.path))}
          onRestore={(session) => void mutate(() => api.restoreSession(session.path))}
          onDelete={(session) => void mutate(() => api.deleteSession(session.path))}
        />

        <div className="rail__foot">
          <div className="rail__meta">
            {facts?.version ? `v${facts.version}` : ""}
            {statusDirectory ? ` · ${basename(statusDirectory)}` : ""}
          </div>
        </div>
      </nav>

      <main className="main">
        <TopBar
          title={title}
          model={modelLabel}
          thinking={models?.thinking}
          connected={connected}
          running={running}
          filesOpen={files}
          onOpenSettings={() => setSettings(true)}
          onToggleFiles={() => setFiles((value) => !value)}
        />

        <div className="workspace" data-drawer={files ? "open" : "closed"}>
          <div className="chat">
            <ChatWindow blocks={blocks} running={running} />

            {approvals.length > 0 ? (
              <div className="approvals">
                {approvals.map((question) => (
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

            <Composer disabled={running} onSend={(text) => void send(text)} />
          </div>

          <FileDrawer open={files} onClose={() => setFiles(false)} />
        </div>
      </main>

      <SettingsSheet
        open={settings}
        theme={theme}
        onTheme={setTheme}
        onClose={() => setSettings(false)}
        onModelChanged={() => void refreshModels()}
      />
    </div>
  );
}

function basename(path: string): string {
  const parts = path.replace(/\/+$/, "").split("/");
  return parts[parts.length - 1] || path;
}
