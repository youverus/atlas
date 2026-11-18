// The top bar carries what the window is currently pointed at: which model
// answers, whether the stream is live, and the two toggles that change the
// layout. Nothing here is a command except the chips, which open the sheet.
export function TopBar({
  title,
  model,
  thinking,
  permission,
  connected,
  running,
  filesOpen,
  trajectoryOpen,
  onOpenSettings,
  onToggleFiles,
  onToggleTrajectory,
}: {
  title: string;
  model: string;
  thinking?: string;
  permission?: { label: string; loud: boolean };
  connected: boolean;
  running: boolean;
  filesOpen: boolean;
  trajectoryOpen: boolean;
  onOpenSettings: () => void;
  onToggleFiles: () => void;
  onToggleTrajectory: () => void;
}) {
  return (
    <header className="topbar">
      <span className="topbar__title">{title}</span>
      <span className="topbar__facts">
        {running ? <span className="chip chip--accent">运行中</span> : null}
        <button type="button" className="chip chip--button" onClick={onOpenSettings}>
          {model || "未配置模型"}
          {thinking ? ` · ${thinking}` : ""}
        </button>
        {permission ? (
          <button
            type="button"
            className={permission.loud ? "chip chip--button chip--warn" : "chip chip--button"}
            onClick={onOpenSettings}
            title="权限模式"
          >
            {permission.label}
          </button>
        ) : null}
        <span className="chip" title={connected ? "事件流已连接" : "事件流断开，正在重连"}>
          <span className={connected ? "dot" : "dot dot--off"} />
          {connected ? "在线" : "重连中"}
        </span>
        <button
          type="button"
          className="icon-button"
          onClick={onToggleFiles}
          aria-pressed={filesOpen}
          title="文件"
          aria-label="文件"
        >
          ⌸
        </button>
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
      </span>
    </header>
  );
}
