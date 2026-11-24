import { useEffect, useState } from "react";
import type { ProjectGroup, SessionInfo } from "../api";

function groupKey(group: ProjectGroup): string {
  return `${group.deviceId || "local"}:${group.path}`;
}

function remoteStateSuffix(state?: string): string {
  if (state === "online" || !state) return "";
  if (state === "connecting") return "连接中";
  if (state === "error") return "异常";
  return "离线";
}

// The session rail.
//
// It is drawn per project because a conversation is: a session file records the
// directory it runs in, so "which project is this" has one answer and no tag to
// maintain. The first group is the folder conversations start in, and it is
// always there — a rail that emptied itself when the last project was removed
// would leave no way to start anything.
//
// A row is a switch, and its second half is a menu: archiving and deleting are
// rare and destructive, so they live behind a target small enough that it cannot
// be hit while reaching for the row itself.
export function SessionRail({
  groups,
  archived,
  current,
  pendingDir,
  pendingKey,
  runningPaths = [],
  approvalPaths = [],
  onToggleArchived,
  onOpen,
  onNew,
  onNewIn,
  onArchive,
  onRestore,
  onDelete,
  onAddProject,
  onRemoveProject,
  onConnectDevice,
}: {
  groups: ProjectGroup[];
  archived: boolean;
  current?: string;
  /** Folder of a conversation started but not yet written, so it has no row of its own. */
  pendingDir?: string;
  /** Scoped key (`deviceId:path`) matching pendingDir when local/remote paths share spellings. */
  pendingKey?: string;
  runningPaths?: string[];
  approvalPaths?: string[];
  onToggleArchived: () => void;
  onOpen: (session: SessionInfo) => void;
  onNew: () => void;
  onNewIn: (cwd: string, deviceId?: string) => void;
  onArchive: (session: SessionInfo) => void;
  onRestore: (session: SessionInfo) => void;
  onDelete: (session: SessionInfo) => void;
  onAddProject: () => void;
  onRemoveProject: (path: string, deviceId?: string) => void;
  onConnectDevice?: (deviceId: string) => Promise<void> | void;
}) {
  const [menu, setMenu] = useState<string | null>(null);
  const [projectMenu, setProjectMenu] = useState<string | null>(null);
  const [connectingDevice, setConnectingDevice] = useState<string | null>(null);
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>(() => {
    try {
      const raw = window.localStorage.getItem("atlas.rail.collapsed");
      return raw ? (JSON.parse(raw) as Record<string, boolean>) : {};
    } catch {
      return {};
    }
  });

  const toggleProject = (key: string) => {
    setCollapsed((prev) => {
      const next = { ...prev, [key]: !prev[key] };
      try {
        window.localStorage.setItem("atlas.rail.collapsed", JSON.stringify(next));
      } catch {
        // Ignore storage errors (private mode / quota).
      }
      return next;
    });
  };

  const expandProject = (key: string) => {
    setCollapsed((prev) => {
      if (!prev[key]) return prev;
      const next = { ...prev, [key]: false };
      try {
        window.localStorage.setItem("atlas.rail.collapsed", JSON.stringify(next));
      } catch {
        // Ignore storage errors.
      }
      return next;
    });
  };

  useEffect(() => {
    setMenu(null);
    setProjectMenu(null);
  }, [archived]);

  // A menu is dismissed by walking away from it, not by finding the same small
  // target again. The listener is captured so it runs before the row's own
  // click: closing first is what keeps "open the row" and "close the menu" from
  // fighting over one event, and a click inside the menu or on the toggle is
  // left to the handlers that already own it.
  useEffect(() => {
    if (!menu && !projectMenu) return;
    const dismiss = (event: Event) => {
      const target = event.target as HTMLElement | null;
      if (target?.closest(".menu, .session-row__more, .project-group__action")) return;
      setMenu(null);
      setProjectMenu(null);
    };
    const escape = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      setMenu(null);
      setProjectMenu(null);
    };
    document.addEventListener("pointerdown", dismiss, true);
    document.addEventListener("keydown", escape);
    return () => {
      document.removeEventListener("pointerdown", dismiss, true);
      document.removeEventListener("keydown", escape);
    };
  }, [menu, projectMenu]);

  const openAddProject = () => {
    setMenu(null);
    setProjectMenu(null);
    onAddProject();
  };

  const handleConnect = async (devId: string) => {
    if (!onConnectDevice || connectingDevice) return;
    setConnectingDevice(devId);
    try {
      await onConnectDevice(devId);
    } finally {
      setConnectingDevice(null);
    }
  };

  return (
    <>
      <div className="rail__actions">
        <button type="button" className="button-primary" onClick={onNew}>
          ＋ 新对话
        </button>
      </div>

      <div className="rail__scroll">
        <div className="rail__label">
          <div className="rail__label-title">
            <span>{archived ? "已归档" : "项目"}</span>
            {archived ? null : (
              <button
                type="button"
                className="icon-button"
                onClick={openAddProject}
                title="添加项目"
                aria-label="添加项目"
                style={{ width: 20, height: 20 }}
              >
                ＋
              </button>
            )}
          </div>
          <button
            type="button"
            className="icon-button"
            onClick={onToggleArchived}
            title={archived ? "返回对话列表" : "查看归档"}
            aria-label={archived ? "返回对话列表" : "查看归档"}
            style={{ width: 20, height: 20 }}
          >
            {archived ? "←" : "⌸"}
          </button>
        </div>

        {groups.length === 0 ? (
          <div className="rail__empty">{archived ? "没有归档的对话。" : "还没有项目。"}</div>
        ) : null}

        {groups.map((group) => {
          const gKey = groupKey(group);
          const isRemote = Boolean(group.deviceId && group.deviceId !== "local");
          const isCollapsed = Boolean(collapsed[gKey]);
          const isPending = pendingKey ? gKey === pendingKey : !isRemote && group.path === pendingDir;
          const offlineRemote = isRemote && group.deviceState !== "online";
          const stateSuffix = remoteStateSuffix(group.deviceState);
          const devBusy = connectingDevice === group.deviceId;
          return (
            <section className="project-group" key={gKey} data-collapsed={isCollapsed ? "true" : "false"}>
              <div
                className="project-group__head"
                data-menu-open={projectMenu === gKey ? "true" : undefined}
              >
                <button
                  type="button"
                  className="project-group__toggle"
                  aria-expanded={!isCollapsed}
                  title={isCollapsed ? "展开项目会话" : "收起项目会话"}
                  onClick={() => toggleProject(gKey)}
                >
                  <span className="project-group__icon" aria-hidden="true">
                    {isRemote ? <ServerIcon /> : <FolderIcon open={!isCollapsed} />}
                  </span>
                  <span className="project-group__name" title={group.path}>
                    {group.name}
                  </span>
                  {group.default ? <span className="project-group__tag">默认</span> : null}
                  {isRemote ? (
                    <span
                      className="project-group__tag"
                      data-device-state={group.deviceState || "offline"}
                      data-warn={group.deviceState === "error" ? "true" : undefined}
                      title={group.remoteError || `${group.deviceName || group.deviceId} (${group.path})`}
                    >
                      {group.deviceName || group.deviceId}
                      {stateSuffix ? `·${stateSuffix}` : ""}
                    </span>
                  ) : null}
                  {group.missing ? (
                    <span className="project-group__tag" data-warn="true">
                      目录已不在
                    </span>
                  ) : null}
                  {isCollapsed && group.sessions.length > 0 ? (
                    <span className="project-group__count">{group.sessions.length}</span>
                  ) : null}
                </button>
                <div className="project-group__actions">
                  <button
                    type="button"
                    className="icon-button project-group__action"
                    title="在这个项目里开新对话"
                    aria-label="在这个项目里开新对话"
                    onClick={() => {
                      expandProject(gKey);
                      onNewIn(group.path, group.deviceId);
                    }}
                  >
                    ＋
                  </button>
                  {group.default ? null : (
                    <button
                      type="button"
                      className="icon-button project-group__action"
                      title="项目操作"
                      aria-label="项目操作"
                      onClick={() =>
                        setProjectMenu(projectMenu === gKey ? null : gKey)
                      }
                    >
                      ⋯
                    </button>
                  )}
                </div>
              </div>

              {projectMenu === gKey ? (
                <div className="menu" style={{ left: 0, right: 0, top: 34 }}>
                  {offlineRemote && group.deviceId && onConnectDevice ? (
                    <button
                      type="button"
                      disabled={Boolean(connectingDevice)}
                      onClick={() => {
                        setProjectMenu(null);
                        void handleConnect(group.deviceId!);
                      }}
                    >
                      {devBusy ? "连接中…" : `连接 ${group.deviceName || group.deviceId}`}
                    </button>
                  ) : null}
                  <button
                    type="button"
                    onClick={() => {
                      setProjectMenu(null);
                      onRemoveProject(group.path, group.deviceId);
                    }}
                  >
                    移除项目
                  </button>
                </div>
              ) : null}

              {!isCollapsed ? (
                <div className="project-group__items">
                  {offlineRemote && group.sessions.length === 0 && !isPending ? (
                    <div className="project-group__empty project-group__empty--remote">
                      <span>{group.deviceState === "error" ? "远程连接失败" : "远程设备未连接"}</span>
                      {group.deviceId && onConnectDevice ? (
                        <button
                          type="button"
                          className="project-dialog__link"
                          disabled={Boolean(connectingDevice)}
                          onClick={() => void handleConnect(group.deviceId!)}
                        >
                          {devBusy ? "连接中…" : "连接"}
                        </button>
                      ) : null}
                    </div>
                  ) : group.sessions.length === 0 && (archived || !isPending) ? (
                    <div className="project-group__empty">
                      {archived ? "没有归档。" : "还没有对话。"}
                    </div>
                  ) : null}

                  {/* A conversation has no file until its first message, so the row it
                      will become is drawn here: the rail is where "which project am I
                      talking in" is answered, and an unanswered click looks broken. */}
                  {!archived && isPending ? (
                    <div className="session-row session-row--pending" data-current="true">
                      <span className="session-row__lead" aria-hidden="true" />
                      <span className="session-row__body">
                        <span className="session-row__title">新对话</span>
                        <span className="session-row__meta">还没有消息</span>
                      </span>
                    </div>
                  ) : null}

                  {group.sessions.map((session) => {
                    const isRunning = runningPaths.includes(session.path);
                    const needsApproval = approvalPaths.includes(session.path);
                    const hasLiveState = needsApproval || isRunning;
                    return (
                    <div
                      className="session-row__slot"
                      key={session.path}
                      data-menu-open={menu === session.path ? "true" : undefined}
                    >
                      <button
                        type="button"
                        className="session-row"
                        data-current={session.path === current ? "true" : "false"}
                        onClick={() => onOpen(session)}
                      >
                        <span className="session-row__lead" aria-hidden={!hasLiveState || undefined}>
                          {hasLiveState ? (
                            <span
                              className="session-row__live"
                              data-state={needsApproval ? "approval" : "running"}
                              title={needsApproval ? "等待工具确认" : "正在后台运行"}
                              aria-label={needsApproval ? "等待工具确认" : "正在后台运行"}
                            />
                          ) : null}
                        </span>
                        <span className="session-row__body">
                          <span className="session-row__title">{title(session)}</span>
                          <span className="session-row__meta">{meta(session)}</span>
                        </span>
                        <span
                          role="button"
                          tabIndex={0}
                          className="session-row__more icon-button"
                          aria-label="更多操作"
                          onClick={(event) => {
                            event.stopPropagation();
                            setMenu(menu === session.path ? null : session.path);
                          }}
                          onKeyDown={(event) => {
                            if (event.key === "Enter" || event.key === " ") {
                              event.preventDefault();
                              event.stopPropagation();
                              setMenu(menu === session.path ? null : session.path);
                            }
                          }}
                        >
                          ⋯
                        </span>
                      </button>

                      {menu === session.path ? (
                        <div className="menu" style={{ right: 4, top: 30 }}>
                          {archived || session.archived ? (
                            <>
                              <button
                                type="button"
                                onClick={() => {
                                  setMenu(null);
                                  onRestore(session);
                                }}
                              >
                                恢复
                              </button>
                              <div className="menu__seam" />
                              <button
                                type="button"
                                data-danger="true"
                                onClick={() => {
                                  setMenu(null);
                                  onDelete(session);
                                }}
                              >
                                删除
                              </button>
                            </>
                          ) : (
                            <button
                              type="button"
                              onClick={() => {
                                setMenu(null);
                                onArchive(session);
                              }}
                            >
                              归档
                            </button>
                          )}
                        </div>
                      ) : null}
                    </div>
                    );
                  })}
                </div>
              ) : null}
            </section>
          );
        })}
      </div>
    </>
  );
}

// title prefers a name the reader gave, then the first thing they said, and only
// then the id: an empty row is a row nobody can pick on purpose.
function title(session: SessionInfo): string {
  if (session.name) return session.name;
  const first = (session.firstMessage ?? "").replace(/\s+/g, " ").trim();
  if (first) return first.length > 60 ? `${first.slice(0, 60)}…` : first;
  return session.id || "新对话";
}

function meta(session: SessionInfo): string {
  const parts: string[] = [];
  if (session.modified) parts.push(shortDate(session.modified));
  if (session.messages > 0) parts.push(`${session.messages} 条`);
  return parts.join(" · ");
}

function shortDate(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const today = new Date();
  const sameDay =
    date.getFullYear() === today.getFullYear() &&
    date.getMonth() === today.getMonth() &&
    date.getDate() === today.getDate();
  if (sameDay) {
    return `${String(date.getHours()).padStart(2, "0")}:${String(date.getMinutes()).padStart(2, "0")}`;
  }
  return `${date.getMonth() + 1}/${date.getDate()}`;
}

function FolderIcon({ open }: { open: boolean }) {
  return (
    <svg
      width="16"
      height="16"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.75"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      {open ? (
        <path d="m6 14 1.5-2.9A2 2 0 0 1 9.24 10H20a2 2 0 0 1 1.94 2.5l-1.54 6a2 2 0 0 1-1.95 1.5H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h3.9a2 2 0 0 1 1.69.9l.81 1.2a2 2 0 0 0 1.67.9H18a2 2 0 0 1 2 2v2" />
      ) : (
        <path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z" />
      )}
    </svg>
  );
}

function ServerIcon() {
  return (
    <svg
      width="16"
      height="16"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.75"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <rect width="20" height="8" x="2" y="2" rx="2" ry="2" />
      <rect width="20" height="8" x="2" y="14" rx="2" ry="2" />
      <line x1="6" x2="6.01" y1="6" y2="6" />
      <line x1="6" x2="6.01" y1="18" y2="18" />
    </svg>
  );
}
