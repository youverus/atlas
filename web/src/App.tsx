import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  api,
  ApiError,
  type ForkPoint,
  type ModelList,
  type PendingQuestion,
  type PermissionState,
  type ProjectGroup,
  type State,
  type TrajectoryRow,
} from "./api";
import { ApprovalCard } from "./components/ApprovalCard";
import { ChatWindow } from "./components/ChatWindow";
import { Composer } from "./components/Composer";
import { FileDrawer } from "./components/FileDrawer";
import { ForkDialog } from "./components/ForkDialog";
import { SessionRail } from "./components/SessionRail";
import { SettingsSheet } from "./components/SettingsSheet";
import { SidePanel } from "./components/SidePanel";
import { TopBar } from "./components/TopBar";
import { TrajectoryView } from "./components/TrajectoryView";
import * as commands from "./lib/commands";
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
  const [permission, setPermission] = useState<PermissionState | null>(null);
  const [theme, setTheme] = useTheme();
  const [settings, setSettings] = useState(false);
  const [files, setFiles] = useState(false);
  const [notice, setNotice] = useState("");
  // The fork sheet: open while the reader is choosing where to branch and what
  // to call it, busy while the kernel is copying. They are two states because
  // the copy can refuse — an archived conversation cannot be branched — and the
  // sheet has to stay up to say so. The points come from the kernel when the
  // sheet opens rather than with the page, because the conversation can change
  // under both of them and the list is only asked about the one in view.
  const [forking, setForking] = useState(false);
  const [forkBusy, setForkBusy] = useState(false);
  const [forkPoints, setForkPoints] = useState<ForkPoint[]>([]);
  // Text handed to the input box: a fork in front of a prompt gives that prompt
  // back, and this is how it gets there.
  const [seed, setSeed] = useState<{ text: string } | null>(null);
  // 轨迹是同一段会话的第二种读法，所以它是一个模式而不是第二个页面：打开时
  // 阅读列换成一列记录，关掉就回到对话。数据按需取，不跟流——它读的是已经写
  // 下的东西，一条还在跑的记录本来就不完整。
  const [trajectory, setTrajectory] = useState<TrajectoryRow[]>([]);
  const [trajectoryOpen, setTrajectoryOpen] = useState(false);
  const [trajectoryLoading, setTrajectoryLoading] = useState(false);
  const [trajectoryError, setTrajectoryError] = useState("");
  // 侧对话：右侧那根常驻的列。它只活在进程内存里，所以页面上的每一块都来自事件
  // 流或者 /api/side——重新加载要先问一遍，这是它和主对话唯一不同的地方。
  // inherited 是它开头有多少块继承自主对话，面板据它放一条分隔线。
  const [sideOpen, setSideOpen] = useState(false);
  const [sideBlocks, setSideBlocks] = useState<Block[]>([]);
  const [sideRunning, setSideRunning] = useState(false);
  const [sideInherited, setSideInherited] = useState(0);

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

  // 侧对话同一套：两个 agent 同时推事件，各自用一个 ref，折叠才不会互相覆盖。
  const sideCurrent = useRef<Block[]>([]);
  const commitSide = useCallback((next: Block[]) => {
    sideCurrent.current = next;
    setSideBlocks(next);
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

  // The posture is fetched on its own rather than folded into the model list:
  // the two are different questions, and a kernel with a gate but no models
  // (or the reverse) has to be able to answer one of them.
  const refreshPermission = useCallback(async () => {
    try {
      setPermission(await api.permission());
    } catch {
      setPermission(null);
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
    void refreshPermission();
    void loadTranscript();
    void loadSide();
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
        // The receipt rides on the same frame as "stopped", so one commit
        // leaves the transcript settled and accounted for. Folding it in a
        // second call would drop one of the two whenever a burst lands between
        // them.
        commit(fold.receipt(fold.settle(current.current), status.receipt ?? []));
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
    // 侧对话走自己的两个名字。折叠函数是同一个：一段对话怎么折和它是哪一段无关。
    onSideAgent: (event) => {
      commitSide(fold.apply(sideCurrent.current, event).blocks);
    },
    onSideStatus: (status) => {
      setSideOpen(status.open);
      setSideRunning(status.running);
      if (status.error) {
        commitSide(fold.notice(sideCurrent.current, "error", status.error));
      }
      if (!status.running) {
        commitSide(fold.receipt(fold.settle(sideCurrent.current), status.receipt ?? []));
      }
    },
  });

  // A line that starts with a slash is a command, not a prompt. It is run here
  // rather than posted: sending "/model" to the model would be a question about
  // a command it has never heard of, and the palette that just completed the
  // line would have taught the reader something false. An unknown name answers
  // with the table instead of guessing, which is the only way the palette stays
  // the place that is right about what exists.
  const runCommand = async (name: string, argument: string) => {
    const known = commands.find(name);
    if (!known) {
      commit(
        fold.notice(current.current, "notice", `窗口没有 ${name} 这个命令。\n\n${commands.helpText()}`),
      );
      return;
    }
    if (argument && !known.usage) {
      commit(fold.notice(current.current, "notice", `${name} 不带参数`));
      return;
    }
    // Everything below moves the conversation, and the kernel holds one turn at
    // a time: switching under a running turn is a crash shape, not a feature.
    if (running) {
      commit(fold.notice(current.current, "notice", "这一轮还在跑，等它结束再切"));
      return;
    }
    switch (known.name) {
      case "/help":
        commit(fold.notice(current.current, "notice", commands.helpText()));
        return;
      case "/compact":
        // 同步等它做完，理由在 api.compact 上：压缩改的是下一轮要读的上下文。
        // 过程本身写在对话里（compaction_start/end 折成两行 notice，带前后的
        // token 数），所以这里不另外报一次进度。
        //
        // 失败也一样：内核把原因写在 compaction_end 里，「压缩失败：…」就落在
        // 压缩开始的那一行上。这里只在请求根本没跑起来时说话（闩被一轮占着、
        // 或者这个构建没有内核），那种情况没有任何事件可以承载这条消息。
        setRunning(true);
        try {
          await api.compact(argument);
        } catch (cause) {
          if (cause instanceof ApiError && cause.status === 409) {
            commit(fold.notice(current.current, "notice", "这一轮还在跑，等它结束再压缩"));
            return;
          }
          commit(
            fold.notice(current.current, "error", cause instanceof Error ? cause.message : String(cause)),
          );
        } finally {
          // 失败时没有 status 帧可等，成功时这一行和帧说的是同一件事。
          setRunning(false);
        }
        return;
      case "/side":
        if (argument === "off") {
          await closeSide();
          return;
        }
        if (argument) {
          commit(fold.notice(current.current, "notice", "/side 只认 off：/side 开，/side off 关"));
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
      case "/permission":
        // The sheet holds both, and it is the window's picker for both.
        setSettings(true);
        return;
      case "/fork":
        // The same action as the branch button at the end of the conversation:
        // one intention, two places to reach for it.
        openFork();
        return;
    }
  };

  const send = async (text: string) => {
    const command = commands.parse(text);
    if (command) {
      await runCommand(command.name, command.argument);
      return;
    }
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

  // ---------- 侧对话 ----------
  //
  // 三个动作对着三条路由。/api/side 的开是幂等的：终端的 BeginSide 会丢掉上一
  // 个侧对话（/side 在那儿就是「重来」），但页面重绘或者读者再敲一次 /side 不是
  // 在要求把正在读的会话扔了，所以服务端先看有没有开着。

  const openSide = async () => {
    try {
      const view = await api.openSide();
      setSideOpen(true);
      setSideInherited(view.inherited);
      setSideRunning(view.running);
      commitSide(fold.replay(view.blocks));
    } catch (cause) {
      setNotice(cause instanceof Error ? cause.message : String(cause));
    }
  };

  // 关掉是丢掉：这里没有「收起但留着」的第三种状态。留着要多一条「重开」的路
  // 由和一份谁也不再读的内存，而终端里 /side off 的意思就是不要了。
  const closeSide = async () => {
    try {
      await api.closeSide();
      setSideOpen(false);
      setSideInherited(0);
      setSideRunning(false);
      commitSide([]);
    } catch (cause) {
      setNotice(cause instanceof Error ? cause.message : String(cause));
    }
  };

  // 问一句。面板没开就先开，再把话发过去——/btw 在终端里就是这个顺序，读者不
  // 需要先开一次面板才能问。
  const askSide = async (text: string) => {
    const prompt = text.trim();
    if (!prompt) return;
    try {
      if (!sideOpen) {
        const view = await api.openSide();
        setSideOpen(true);
        setSideInherited(view.inherited);
        commitSide(fold.replay(view.blocks));
      }
      commitSide(fold.prompt(sideCurrent.current, prompt));
      setSideRunning(true);
      await api.sideAsk(prompt);
    } catch (cause) {
      setSideRunning(false);
      commitSide(fold.notice(sideCurrent.current, "error", cause instanceof Error ? cause.message : String(cause)));
    }
  };

  // 面板里那个输入框收到的一行。它不是主输入框：这里只认终端那张侧对话白名单
  // （/btw 与 /side off），别的命令回主对话的输入框敲——在面板里执行会改到侧对
  // 话已经不打算指的那一段会话。
  const sendSide = async (text: string) => {
    const command = commands.parse(text);
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
      commitSide(fold.notice(sideCurrent.current, "notice", "面板已经开着。问一句，或者 /side off 关掉它"));
      return;
    }
    commitSide(
      fold.notice(
        sideCurrent.current,
        "notice",
        `${command.name} 在侧对话里不跑。回主对话的输入框敲；这里只认 /btw 和 /side off`,
      ),
    );
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
      // 换会话时内核把侧对话一起丢了（它不属于任何一段会话），面板得跟着丢。
      // 猜是不行的：这里问一遍服务端，答案就是内核此刻的状态。
      void loadSide();
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

  // 侧对话的第一次读：进程可能已经带着一个开着的侧对话（终端开过、或者页面重载
  // 过），而它不是磁盘上的东西，重新加载必须先问一遍服务端。
  const loadSide = useCallback(async () => {
    try {
      const view = await api.side();
      setSideOpen(view.open);
      setSideInherited(view.inherited);
      setSideRunning(view.running);
      commitSide(fold.replay(view.blocks));
    } catch {
      setSideOpen(false);
      commitSide([]);
    }
  }, [commitSide]);

  // 轨迹按需取：它比转录大（不截断、带用量），而大多数时候读者并不在看它。错误
  // 要显示出来而不是留一列空白——空列表会被读成「这段会话没有记录」。
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
      // 同 switchSession：新会话也一样，内核那边没有侧对话了。
      void loadSide();
    } catch (cause) {
      setNotice(cause instanceof Error ? cause.message : String(cause));
    }
  };

  // A fork continues this conversation in a new session holding a copy of the
  // branch up to the chosen point. The window then reads the copy back: for a
  // fork at the end it holds the same messages, so the page would otherwise look
  // like nothing happened, and the notice says which branch the next message
  // will land in. A fork in front of a prompt is visibly shorter, so the notice
  // also says what happened to the message that was left behind.
  const forkSession = async (entryID: string, name: string) => {
    setForkBusy(true);
    try {
      const result = await api.forkSession(entryID, name);
      const replayed = fold.replay(await api.transcript());
      commit(fold.notice(replayed, "notice", forkNotice(entryID, name, forkPoints)));
      setForking(false);
      setDraftDir("");
      if (result?.text) setSeed({ text: result.text });
      await refreshSessions();
      setFacts(await api.state());
      // 分叉换了会话文件，内核同样收掉了侧对话。
      void loadSide();
    } catch (cause) {
      setNotice(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setForkBusy(false);
    }
  };

  // openFork refuses the two states a fork cannot start from, with the reason:
  // an empty conversation has nothing to copy, and the kernel holds one turn at
  // a time so a branch under a running one would be two writers on one file. The
  // list of points is asked for here rather than kept from the last time, so a
  // message said since then is a point the reader can choose.
  const openFork = async () => {
    if (running) {
      setNotice("这一轮还在跑，等它结束再分叉");
      return;
    }
    if (current.current.length === 0) {
      setNotice("还没有消息，先问一句再分叉");
      return;
    }
    try {
      setForkPoints(await api.forkPoints());
      setForking(true);
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

  // The row in the rail that stands for the conversation in view. A fork needs
  // it twice: an archived conversation cannot be branched (the kernel refuses
  // writes to one), and its name is the seed for the branch's own.
  const active = useMemo(() => sessions.find((session) => session.current), [sessions]);

  // 轨迹只在打开时才取，并且在这两件事之后重取：换了一段会话（回答的必须是屏幕
  // 上这一段），或者一轮跑完了（记录刚写完）。关着的时候这个 effect 什么都不做，
  // 所以它不会跟着每一轮变成一次多余的请求。
  useEffect(() => {
    if (!trajectoryOpen || running) return;
    void loadTrajectory();
  }, [trajectoryOpen, running, currentPath, loadTrajectory]);

  const title = useMemo(() => {
    if (!active) return "atlas";
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
          permission={
            permission
              ? { label: labelOf(permission), loud: permission.mode === "bypassPermissions" || permission.mode === "off" }
              : undefined
          }
          connected={connected}
          running={running}
          filesOpen={files}
          trajectoryOpen={trajectoryOpen}
          onOpenSettings={() => setSettings(true)}
          onToggleFiles={() => setFiles((value) => !value)}
          onToggleTrajectory={() => setTrajectoryOpen((value) => !value)}
        />

        <div className="workspace" data-drawer={files ? "open" : "closed"} data-side={sideOpen ? "open" : "closed"}>
          <div className="chat">
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
                onFork={openFork}
                canFork={Boolean(active && !active.archived)}
              />
            )}

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

            <Composer disabled={running} onSend={(text) => void send(text)} seed={seed} />
          </div>

          {sideOpen ? (
            // 排在对话和文件抽屉中间：网格的列序就是 DOM 的顺序，而侧对话要贴着
            // 主对话——它说的是同一段会话的另一半，文件抽屉才在最外面。
            <SidePanel
              blocks={sideBlocks}
              inherited={sideInherited}
              running={sideRunning}
              onAsk={(text) => void sendSide(text)}
              onClose={() => void closeSide()}
            />
          ) : null}

          <FileDrawer open={files} onClose={() => setFiles(false)} />
        </div>
      </main>

      <SettingsSheet
        open={settings}
        theme={theme}
        onTheme={setTheme}
        onClose={() => setSettings(false)}
        onModelChanged={() => void refreshModels()}
        onPermissionChanged={() => void refreshPermission()}
      />

      <ForkDialog
        open={forking}
        points={forkPoints}
        // The branch arrives in the rail next to its original, so a name that
        // repeats the original makes the two rows indistinguishable. Seeding it
        // with "1 …" means the default already says which one is new.
        suggested={forkName}
        busy={forkBusy}
        onCancel={() => setForking(false)}
        onConfirm={(entryID, name) => void forkSession(entryID, name)}
      />
    </div>
  );
}

function basename(path: string): string {
  const parts = path.replace(/\/+$/, "").split("/");
  return parts[parts.length - 1] || path;
}

// forkNotice says what just happened to the conversation. Two facts matter: which
// branch the next message lands in, and — when the cut was in the middle — that
// the rest of it is gone and the message that was branched off came back as a
// draft. Without the second, a fork in front of a prompt looks like the window
// lost the second half of the conversation.
function forkNotice(entryID: string, name: string, points: ForkPoint[]): string {
  const branch = name ? `已分叉：「${name}」` : "已分叉";
  if (!entryID) return `${branch}，接下来的消息写进这一条分支`;
  const point = points.find((item) => (item.entryId ?? "") === entryID);
  const at = point ? `第 ${point.index} 条` : "选中的那条";
  return `${branch}，只保留 ${at}之前的内容；那句话已放回输入框，改完直接发`;
}

// The reader-facing name of the posture in force. An unknown mode is printed as
// it arrived: a posture this build cannot name is a fact about the kernel, not
// something to hide behind "unknown".
function labelOf(state: PermissionState): string {
  return state.modes.find((mode) => mode.value === state.mode)?.label ?? state.mode;
}
