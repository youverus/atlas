// The top bar carries what the window is currently pointed at: which conversation,
// whether a turn is running, and the two toggles that change the layout. The
// model and the posture are not here — they sit under the input box, next to the
// place the next message is written, because that is the question they answer.

/** Sidebar collapse/expand icon matching zcode's PanelLeft/PanelRight affordances. */
export function SidebarToggleIcon({
  collapsed,
  side = "left",
}: {
  collapsed: boolean;
  side?: "left" | "right";
}) {
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
      aria-hidden="true"
    >
      <g transform={side === "right" ? "translate(24 0) scale(-1 1)" : undefined}>
        <rect width="18" height="18" x="3" y="3" rx="2.5" />
        <path d="M9 3v18" />
        {collapsed ? <path d="m14 9 3 3-3 3" /> : <path d="m16 15-3-3 3-3" />}
      </g>
    </svg>
  );
}

/** Terminal launch icon matching zcode's SquareTerminalIcon affordance. */
export function TerminalIcon() {
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
      aria-hidden="true"
    >
      <rect width="18" height="18" x="3" y="3" rx="2.5" />
      <path d="m7.5 9 3 3-3 3" />
      <path d="M12.5 15h4" />
    </svg>
  );
}

export function TopBar({
  title,
  directory,
  deviceName,
  connected,
  running,
  archived = false,
  railCollapsed = false,
  filesOpen,
  artifactsOpen = false,
  artifactCount = 0,
  terminalOpen = false,
  trajectoryOpen,
  onExpandRail,
  onToggleTerminal,
  onToggleFiles,
  onToggleArtifacts,
  onToggleTrajectory,
}: {
  title: string;
  directory?: string;
  deviceName?: string;
  /** 事件流是否连着。只有断开时才画东西：一个天天写着「在线」的框是两个状态里
   *  只有一个需要看的那一种噪声。 */
  connected: boolean;
  running: boolean;
  archived?: boolean;
  railCollapsed?: boolean;
  filesOpen: boolean;
  artifactsOpen?: boolean;
  artifactCount?: number;
  terminalOpen?: boolean;
  trajectoryOpen: boolean;
  onExpandRail?: () => void;
  onToggleTerminal?: () => void;
  onToggleFiles: () => void;
  onToggleArtifacts?: () => void;
  onToggleTrajectory: () => void;
}) {
  return (
    <header className="topbar">
      {railCollapsed && onExpandRail ? (
        <button
          type="button"
          className="icon-button topbar__rail-toggle"
          onClick={onExpandRail}
          title="展开侧边栏"
          aria-label="展开侧边栏"
        >
          <SidebarToggleIcon collapsed />
        </button>
      ) : null}
      <span className="topbar__title">{title}</span>
      <span className="topbar__facts">
        {deviceName ? (
          <span className="chip" title={directory ? `远程设备 ${deviceName} · ${directory}` : `远程设备 ${deviceName}`}>
            远程 · {deviceName}
          </span>
        ) : null}
        {running ? <span className="chip chip--accent">运行中</span> : null}
        {archived ? (
          <span className="chip" title="已归档会话仅可查看、恢复或删除，无法继续发送消息">
            已归档
          </span>
        ) : null}
        {connected ? null : (
          <span className="chip chip--warn" title="事件流断开，正在重连">
            <span className="dot dot--warn" />
            重连中
          </span>
        )}
        {/* 轨迹是阅读列的第二种读法，所以它挨着文件那个布局开关，而不是放进设置
            面板：读的时候要能一下翻过去再翻回来。 */}
        <button
          type="button"
          className={trajectoryOpen ? "chip chip--button chip--accent" : "chip chip--button"}
          onClick={onToggleTrajectory}
          aria-pressed={trajectoryOpen}
          title="看这段会话记录了什么：每条的事实、顺序与出处"
        >
          轨迹
        </button>
        {onToggleArtifacts ? (
          <button
            type="button"
            className={artifactsOpen ? "chip chip--button chip--accent" : "chip chip--button"}
            onClick={onToggleArtifacts}
            aria-pressed={artifactsOpen}
            title={artifactsOpen ? "收起产物预览" : "打开产物预览"}
          >
            {artifactCount > 0 ? `产物 · ${artifactCount}` : "产物"}
          </button>
        ) : null}
        {onToggleTerminal ? (
          <button
            type="button"
            className="icon-button"
            onClick={onToggleTerminal}
            aria-pressed={terminalOpen}
            disabled={!directory}
            title={terminalOpen ? "收起终端" : directory ? `打开终端 (${directory})` : "打开终端"}
            aria-label={terminalOpen ? "收起终端" : "打开终端"}
          >
            <TerminalIcon />
          </button>
        ) : null}
        <button
          type="button"
          className="icon-button"
          onClick={onToggleFiles}
          aria-pressed={filesOpen}
          title={filesOpen ? "收起文件栏" : "展开文件栏"}
          aria-label={filesOpen ? "收起文件栏" : "展开文件栏"}
        >
          <SidebarToggleIcon collapsed={!filesOpen} side="right" />
        </button>
      </span>
    </header>
  );
}
