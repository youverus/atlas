import { useEffect, useState } from "react";
import type { ProjectGroup, SessionInfo } from "../api";

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
  onToggleArchived,
  onOpen,
  onNew,
  onNewIn,
  onArchive,
  onRestore,
  onDelete,
  onPickProject,
  onAddProjectPath,
  onRemoveProject,
}: {
  groups: ProjectGroup[];
  archived: boolean;
  current?: string;
  /** Folder of a conversation started but not yet written, so it has no row of its own. */
  pendingDir?: string;
  onToggleArchived: () => void;
  onOpen: (session: SessionInfo) => void;
  onNew: () => void;
  onNewIn: (cwd: string) => void;
  onArchive: (session: SessionInfo) => void;
  onRestore: (session: SessionInfo) => void;
  onDelete: (session: SessionInfo) => void;
  /** Resolves false when this build has no folder chooser, so a path is asked for instead. */
  onPickProject: () => Promise<boolean>;
  /** Adds the path the reader typed, when there is no chooser to open. */
  onAddProjectPath: (path: string) => void;
  onRemoveProject: (path: string) => void;
}) {
  const [menu, setMenu] = useState<string | null>(null);
  const [projectMenu, setProjectMenu] = useState<string | null>(null);
  const [asking, setAsking] = useState(false);
  const [draft, setDraft] = useState("");

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

  const pickProject = async () => {
    setMenu(null);
    setProjectMenu(null);
    if (await onPickProject()) return;
    setDraft("");
    setAsking(true);
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
          <span>{archived ? "已归档" : "项目"}</span>
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

        {groups.map((group) => (
          <section className="project-group" key={group.path}>
            <div className="project-group__head">
              <span className="project-group__name" title={group.path}>
                {group.name}
              </span>
              {group.default ? <span className="project-group__tag">默认</span> : null}
              {group.missing ? (
                <span className="project-group__tag" data-warn="true">
                  目录已不在
                </span>
              ) : null}
              <span className="rail__spacer" />
              <button
                type="button"
                className="icon-button project-group__action"
                title="在这个项目里开新对话"
                aria-label="在这个项目里开新对话"
                onClick={() => onNewIn(group.path)}
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
                    setProjectMenu(projectMenu === group.path ? null : group.path)
                  }
                >
                  ⋯
                </button>
              )}
            </div>

            {projectMenu === group.path ? (
              <div className="menu" style={{ left: 8, right: 8 }}>
                <button
                  type="button"
                  onClick={() => {
                    setProjectMenu(null);
                    onRemoveProject(group.path);
                  }}
                >
                  移除项目
                </button>
              </div>
            ) : null}

            {group.sessions.length === 0 && group.path !== pendingDir ? (
              <div className="project-group__empty">
                {archived ? "没有归档。" : "还没有对话。"}
              </div>
            ) : null}

            {/* A conversation has no file until its first message, so the row it
                will become is drawn here: the rail is where "which project am I
                talking in" is answered, and an unanswered click looks broken. */}
            {group.path === pendingDir ? (
              <div className="session-row session-row--pending" data-current="true">
                <span className="session-row__body">
                  <span className="session-row__title">新对话</span>
                  <span className="session-row__meta">还没有消息</span>
                </span>
              </div>
            ) : null}

            {group.sessions.map((session) => (
              <div className="session-row__slot" key={session.path}>
                <button
                  type="button"
                  className="session-row"
                  data-current={session.path === current ? "true" : "false"}
                  onClick={() => onOpen(session)}
                >
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
                  <div className="menu" style={{ right: 4, top: 28 }}>
                    {archived ? (
                      <button
                        type="button"
                        onClick={() => {
                          setMenu(null);
                          onRestore(session);
                        }}
                      >
                        恢复
                      </button>
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
                  </div>
                ) : null}
              </div>
            ))}
          </section>
        ))}

        {archived ? null : asking ? (
          <form
            className="rail__path-form"
            onSubmit={(event) => {
              event.preventDefault();
              const path = draft.trim();
              if (!path) return;
              setAsking(false);
              onAddProjectPath(path);
            }}
          >
            <input
              autoFocus
              className="rail__path-input"
              value={draft}
              placeholder="/绝对路径"
              onChange={(event) => setDraft(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Escape") setAsking(false);
              }}
              aria-label="项目文件夹路径"
            />
            <button type="submit">确定</button>
          </form>
        ) : (
          <button type="button" className="rail__add-project" onClick={() => void pickProject()}>
            ＋ 添加项目
          </button>
        )}
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
