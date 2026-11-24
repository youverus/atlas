import { useCallback, useEffect, useState } from "react";
import { api, type MCPServerInfo, type MCPState } from "../api";

// The right-hand MCP control panel: inspects configured servers, live status,
// and exposed tools without disturbing any active conversation turn.
export function MCPPanel({
  open,
  onClose,
}: {
  open: boolean;
  onClose: () => void;
}) {
  const [state, setState] = useState<MCPState | null>(null);
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const [loading, setLoading] = useState(false);
  const [refreshingServer, setRefreshingServer] = useState<string | null>(null);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      setState(await api.mcp());
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (!open) return;
    void load();
  }, [open, load]);

  if (!open) return null;

  const refresh = async (name = "") => {
    setRefreshingServer(name || "__all__");
    setError("");
    try {
      setState(await api.refreshMCP(name));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setRefreshingServer(null);
    }
  };

  const toggleServer = (name: string) => {
    setExpanded((prev) => ({ ...prev, [name]: !prev[name] }));
  };

  const servers = state?.servers ?? [];
  const connectedCount = servers.filter((server) => server.state === "connected").length;
  const busy = loading || refreshingServer !== null;

  return (
    <aside className="drawer mcp-panel" aria-label="MCP 控制面板">
      <div className="drawer__head">
        <span className="drawer__title">MCP 控制面板</span>
        {servers.length > 0 ? (
          <span className="mcp-panel__summary">
            {connectedCount}/{servers.length} 已连接
          </span>
        ) : null}
        <button
          type="button"
          className="icon-button"
          disabled={busy}
          onClick={() => void (servers.some((s) => s.state === "connected") ? refresh("") : load())}
          aria-label="刷新 MCP"
          title="刷新 MCP 状态与工具列表"
        >
          ↻
        </button>
        <button type="button" className="icon-button" onClick={onClose} aria-label="关闭">
          ✕
        </button>
      </div>

      <div className="drawer__scroll">
        {error ? <div className="notice notice--error mcp-panel__notice">{error}</div> : null}
        {loading && !state ? <div className="rail__empty">读取 MCP 状态中…</div> : null}

        {state && servers.length === 0 ? (
          <div className="mcp-empty">
            <div className="mcp-empty__title">未配置 MCP 服务</div>
            <p className="mcp-empty__desc">
              可在配置文件中的 <code>mcpServers</code> 字段添加服务，不会打断当前会话。
            </p>
            {state.configPath ? <div className="mcp-empty__path">{state.configPath}</div> : null}
          </div>
        ) : null}

        <div className="mcp-list">
          {servers.map((server) => {
            const isOpen = expanded[server.name] ?? servers.length <= 2;
            const status = serverStatus(server);
            const toolText =
              server.declared !== server.tools.length && server.declared > 0
                ? `${server.tools.length}/${server.declared} 个工具`
                : `${server.tools.length} 个工具`;

            return (
              <section className="mcp-card" key={server.name} data-state={server.state}>
                <button
                  type="button"
                  className="mcp-card__head"
                  aria-expanded={isOpen}
                  onClick={() => toggleServer(server.name)}
                >
                  <span className={`dot ${status.dotClass}`} />
                  <span className="mcp-card__title">
                    <span className="mcp-card__name">{server.name}</span>
                    {server.version ? <span className="mcp-card__version">{server.version}</span> : null}
                  </span>
                  <span className="mcp-card__badges">
                    {server.transport ? <span className="mcp-badge">{server.transport}</span> : null}
                    <span className="mcp-badge" data-tone={status.tone}>
                      {status.label}
                    </span>
                  </span>
                  <span className="mcp-card__chevron" aria-hidden="true">
                    {isOpen ? "▾" : "▸"}
                  </span>
                </button>

                <div className="mcp-card__sub">
                  <span>{toolText}</span>
                  {server.state === "connected" ? (
                    <button
                      type="button"
                      className="mcp-card__refresh"
                      disabled={busy}
                      onClick={() => void refresh(server.name)}
                    >
                      {refreshingServer === server.name ? "刷新中…" : "刷新工具"}
                    </button>
                  ) : null}
                </div>

                {server.state === "needs_login" ? (
                  <div className="mcp-card__hint">
                    需要 OAuth 授权，请在终端运行 <code>/mcp login {server.name}</code>
                  </div>
                ) : null}
                {server.error ? <div className="mcp-card__error">{server.error}</div> : null}
                {server.refreshError ? (
                  <div className="mcp-card__error">工具列表刷新失败（保留旧列表）: {server.refreshError}</div>
                ) : null}
                {server.missing && server.missing.length > 0 ? (
                  <div className="mcp-card__hint">
                    白名单中的工具未由服务端声明：{server.missing.join(", ")}
                  </div>
                ) : null}

                {isOpen && server.tools.length > 0 ? (
                  <div className="mcp-tools">
                    {server.tools.map((tool) => (
                      <div className="mcp-tool" key={tool.wireName}>
                        <div className="mcp-tool__head">
                          <span className="mcp-tool__name">{tool.name}</span>
                          <span className="mcp-tool__wire" title={tool.wireName}>
                            {tool.wireName}
                          </span>
                        </div>
                        {tool.description ? <div className="mcp-tool__desc">{tool.description}</div> : null}
                      </div>
                    ))}
                  </div>
                ) : null}

                {isOpen && server.state === "connected" && server.tools.length === 0 ? (
                  <div className="mcp-tools__empty">该服务未暴露可用工具。</div>
                ) : null}
              </section>
            );
          })}
        </div>
      </div>

      <div className="drawer__foot">
        {state?.configPath ? `${basename(state.configPath)} · ` : ""}仅查看与刷新工具，不打断运行中会话
      </div>
    </aside>
  );
}

function serverStatus(server: MCPServerInfo): {
  label: string;
  tone: "ok" | "warn" | "danger";
  dotClass: string;
} {
  if (server.state === "needs_login") {
    return { label: "需授权", tone: "warn", dotClass: "dot--warn" };
  }
  if (server.state === "failed") {
    return { label: "失败", tone: "danger", dotClass: "dot--danger" };
  }
  return { label: "已连接", tone: "ok", dotClass: "" };
}

function basename(path: string): string {
  const parts = path.replace(/\/+$/, "").split("/");
  return parts[parts.length - 1] || path;
}
