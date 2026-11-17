// The top bar carries what the window is currently pointed at: which model
// answers, whether the stream is live, and the two toggles that change the
// layout. Nothing here is a command except the chips, which open the sheet.
export function TopBar({
  title,
  model,
  thinking,
  connected,
  running,
  filesOpen,
  onOpenSettings,
  onToggleFiles,
}: {
  title: string;
  model: string;
  thinking?: string;
  connected: boolean;
  running: boolean;
  filesOpen: boolean;
  onOpenSettings: () => void;
  onToggleFiles: () => void;
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
      </span>
    </header>
  );
}
