import { useCallback, useEffect, useRef, useState } from "react";
import {
  api,
  ApiError,
  type BrowserCapture,
  type BrowserState,
  type BrowserTabInfo,
  type DeviceList,
  type MCPServerInfo,
  type MCPState,
  type ModelConfig,
  type ModelList,
  type PermissionState,
  type PluginState,
  type RemoteStatus,
  type ScheduleList,
  type SkillInfo,
  type SkillState,
} from "../api";
import type { Theme } from "../lib/useTheme";
import { DevicesPanel } from "./DevicesPanel";
import { MarkdownBody } from "./MarkdownBody";
import { ModelConfigEditor, type ModelConfigEditorHandle } from "./ModelConfigEditor";
import { RemotePanel } from "./RemotePanel";
import { SchedulePanel } from "./SchedulePanel";
import { SETTINGS_SECTIONS, type ControlTab } from "./SettingsNav";
import { UsagePanel } from "./UsagePanel";

export type { ControlTab } from "./SettingsNav";

type SkillPreview =
  | { status: "loading" }
  | { status: "loaded"; body: string }
  | { status: "error"; message: string };

// Focused settings detail view covering the right workspace area (.main),
// controlled by the left sidebar's SettingsNav so each capability owns a clean
// standalone control panel.
export function ControlPanel({
  tab,
  onTabChange,
  theme,
  onTheme,
  canRunSkill,
  onUseSkill,
  onOpenSession,
  onClose,
  onModelChanged,
  onPermissionChanged,
  onDevicesChanged,
  onCapabilitiesChanged,
  onConfirmed,
}: {
  tab: ControlTab | null;
  onTabChange: (tab: ControlTab) => void;
  theme: Theme;
  onTheme: (theme: Theme) => void;
  canRunSkill: boolean;
  onUseSkill: (prompt: string) => Promise<void> | void;
  onOpenSession: (path: string) => Promise<void> | void;
  onClose: () => void;
  onModelChanged: () => void;
  onPermissionChanged: () => void;
  onDevicesChanged?: () => void;
  onCapabilitiesChanged: () => void;
  onConfirmed: () => Promise<void> | void;
}) {
  const open = tab !== null;
  const [models, setModels] = useState<ModelList | null>(null);
  const [modelConfig, setModelConfig] = useState<ModelConfig | null>(null);
  const [editingModels, setEditingModels] = useState(false);
  const [modelEditorDirty, setModelEditorDirty] = useState(false);
  const [permission, setPermission] = useState<PermissionState | null>(null);
  const [mcpState, setMcpState] = useState<MCPState | null>(null);
  const [skillState, setSkillState] = useState<SkillState | null>(null);
  const [pluginState, setPluginState] = useState<PluginState | null>(null);
  const [browserState, setBrowserState] = useState<BrowserState | null>(null);
  const [deviceList, setDeviceList] = useState<DeviceList | null>(null);
  const [remoteStatus, setRemoteStatus] = useState<RemoteStatus | null>(null);
  const [scheduleList, setScheduleList] = useState<ScheduleList | null>(null);
  // null 表示这个构建没有壳（/api/shell/pet 回 501），整个开关不画。
  const [petVisible, setPetVisible] = useState<boolean | null>(null);
  const [browserAddress, setBrowserAddress] = useState("");
  const [browserBusy, setBrowserBusy] = useState(false);
  const [browserPreviews, setBrowserPreviews] = useState<Record<string, BrowserCapture>>({});
  const [previewingTabId, setPreviewingTabId] = useState<string | null>(null);
  const [expandedServers, setExpandedServers] = useState<Record<string, boolean>>({});
  const [skillQuery, setSkillQuery] = useState("");
  const [expandedSkills, setExpandedSkills] = useState<Record<string, boolean>>({});
  const [skillPreviews, setSkillPreviews] = useState<Record<string, SkillPreview>>({});
  const [runningSkillName, setRunningSkillName] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [refreshingServer, setRefreshingServer] = useState<string | null>(null);
  const [capabilityBusy, setCapabilityBusy] = useState(false);
  const bodyRef = useRef<HTMLDivElement>(null);
  const modelEditorRef = useRef<ModelConfigEditorHandle | null>(null);

  useEffect(() => {
    if (!editingModels || tab !== "models") {
      setModelEditorDirty(false);
    }
  }, [editingModels, tab]);

  const loadModelConfig = useCallback(async () => {
    try {
      setModelConfig(await api.modelConfig());
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 501) {
        setModelConfig(null);
        return;
      }
      throw cause;
    }
  }, []);

  const loadModels = useCallback(async () => {
    setModels(await api.models());
    await loadModelConfig();
  }, [loadModelConfig]);

  const loadGeneral = useCallback(async () => {
    // 权限与宠物开关各查各的：一边失败不该藏起另一边的控件
    // （-offline 这类没有 Host 的构建里 /api/permission 本就不存在）。
    const permissionTask = api
      .permission()
      .then(setPermission)
      .catch((cause) => {
        if (cause instanceof ApiError && cause.status === 501) return;
        throw cause;
      });
    const petTask = api
      .shellPet()
      .then((state) => setPetVisible(state.visible))
      .catch((cause) => {
        if (cause instanceof ApiError && cause.status === 501) {
          setPetVisible(null);
          return;
        }
        throw cause;
      });
    await Promise.all([permissionTask, petTask]);
  }, []);

  const loadMCP = useCallback(async () => {
    setMcpState(await api.mcp());
  }, []);

  const loadSkills = useCallback(async () => {
    const next = await api.skills();
    setSkillState(next);
    setExpandedSkills({});
    setSkillPreviews({});
  }, []);

  const loadPlugins = useCallback(async () => {
    setPluginState(await api.plugins());
  }, []);

  const loadBrowser = useCallback(async () => {
    setBrowserState(await api.browser());
  }, []);

  const loadRemote = useCallback(async () => {
    setRemoteStatus(await api.remote());
  }, []);

  const loadDevices = useCallback(async () => {
    setDeviceList(await api.devices());
  }, []);

  const loadSchedules = useCallback(async () => {
    const [nextSchedules, nextModels] = await Promise.all([api.schedules(), api.models()]);
    setScheduleList(nextSchedules);
    setModels(nextModels);
  }, []);

  useEffect(() => {
    if (!tab) return;
    let live = true;
    setError("");
    if (tab === "usage") return;
    const task =
      tab === "models"
        ? loadModels()
        : tab === "mcp"
          ? loadMCP()
          : tab === "skills"
            ? loadSkills()
            : tab === "plugins"
              ? loadPlugins()
              : tab === "browser"
                ? loadBrowser()
                : tab === "devices"
                  ? loadDevices()
                  : tab === "remote"
                    ? loadRemote()
                    : tab === "schedules"
                      ? loadSchedules()
                      : loadGeneral();
    void task.catch((cause: unknown) => {
      if (!live) return;
      setError(cause instanceof Error ? cause.message : String(cause));
    });
    return () => {
      live = false;
    };
  }, [tab, loadModels, loadMCP, loadSkills, loadPlugins, loadBrowser, loadDevices, loadRemote, loadSchedules, loadGeneral]);

  useEffect(() => {
    const onBrowserOpened = () => {
      void loadBrowser().catch(() => {});
    };
    window.addEventListener("atlas:browser-opened", onBrowserOpened);
    return () => window.removeEventListener("atlas:browser-opened", onBrowserOpened);
  }, [loadBrowser]);

  useEffect(() => {
    if (!open) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !busy && refreshingServer === null && runningSkillName === null && !browserBusy) {
        onClose();
      }
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [open, busy, refreshingServer, runningSkillName, browserBusy, onClose]);

  const reloadConfigAndClose = async () => {
    setBusy(true);
    setConfirming(true);
    setError("");
    try {
      if (tab === "models" && editingModels && modelEditorRef.current?.isDirty()) {
        const saved = await modelEditorRef.current.saveIfDirty();
        if (!saved) {
          bodyRef.current?.scrollTo({ top: 0 });
          return;
        }
        await loadGeneral();
        onClose();
        return;
      }
      try {
        await api.reloadConfig();
      } catch (cause) {
        if (cause instanceof ApiError && cause.status === 501) {
          onClose();
          return;
        }
        throw cause;
      }
      await Promise.all([loadModels(), loadGeneral()]);
      await onConfirmed();
      onClose();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
      bodyRef.current?.scrollTo({ top: 0 });
    } finally {
      setBusy(false);
      setConfirming(false);
    }
  };

  const chooseModel = async (value: string) => {
    const option = models?.models.find((item) => `${item.provider}/${item.id}` === value);
    if (!option) return;
    setBusy(true);
    setError("");
    try {
      await api.setModel(option.provider, option.id);
      setModels(await api.models());
      onModelChanged();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  };

  const chooseThinking = async (level: string) => {
    setBusy(true);
    setError("");
    try {
      await api.setThinking(level);
      setModels(await api.models());
      onModelChanged();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  };

  const choosePermission = async (value: string) => {
    setBusy(true);
    setError("");
    try {
      setPermission(await api.setPermission(value));
      onPermissionChanged();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  };

  const choosePetVisible = async (visible: boolean) => {
    if (busy || petVisible === null || petVisible === visible) return;
    setBusy(true);
    setError("");
    try {
      // 关闭的拆除在壳里是异步的，接口不回传状态：采用请求值即可，
      // SetPet 幂等，壳最终收敛到它。
      await api.setShellPet(visible);
      setPetVisible(visible);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  };

  const saveModelConfig = async (next: ModelConfig) => {
    setError("");
    await api.saveModelConfig(next);
    await loadModels();
    onModelChanged();
    await onConfirmed();
  };

  const refreshMCP = async (name = "") => {
    setRefreshingServer(name || "__all__");
    setError("");
    try {
      setMcpState(await api.refreshMCP(name));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setRefreshingServer(null);
    }
  };

  const changeCapability = async (kind: "mcp" | "skill", name: string, enabled: boolean) => {
    setCapabilityBusy(true);
    setError("");
    try {
      if (kind === "mcp") {
        setMcpState(await api.setMCPEnabled(name, enabled));
      } else {
        setSkillState(await api.setSkillEnabled(name, enabled));
      }
      onCapabilitiesChanged();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setCapabilityBusy(false);
    }
  };

  const toggleSkillCard = async (key: string, name: string) => {
    const nextOpen = !expandedSkills[key];
    setExpandedSkills((prev) => ({ ...prev, [key]: nextOpen }));
    const status = skillPreviews[key]?.status;
    if (!nextOpen || status === "loaded" || status === "loading") return;

    const pending: SkillPreview = { status: "loading" };
    setSkillPreviews((prev) => ({ ...prev, [key]: pending }));
    let preview: SkillPreview;
    try {
      const detail = await api.skillContent(name);
      preview = { status: "loaded", body: skillMarkdownBody(detail.body) };
    } catch (cause) {
      preview = { status: "error", message: cause instanceof Error ? cause.message : String(cause) };
    }
    setSkillPreviews((prev) => (prev[key] === pending ? { ...prev, [key]: preview } : prev));
  };

  const useSkill = async (skill: SkillInfo) => {
    if (skill.disabled || !canRunSkill || runningSkillName !== null || busy || capabilityBusy) return;
    setRunningSkillName(skill.name);
    setError("");
    try {
      const { prompt } = await api.loadSkillPrompt(skill.name);
      onClose();
      await onUseSkill(prompt);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setRunningSkillName(null);
    }
  };

  const openBrowserAddress = async () => {
    const raw = browserAddress.trim();
    if (!raw || browserBusy) return;
    setBrowserBusy(true);
    setError("");
    try {
      await api.browserOpen(raw);
      setBrowserAddress("");
      setBrowserState(await api.browser());
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBrowserBusy(false);
    }
  };

  const runBrowserAction = async (tabId: string, action: string) => {
    if (browserBusy) return;
    setBrowserBusy(true);
    setError("");
    try {
      const next = await api.browserAction(tabId, action);
      setBrowserState(next);
      if (action === "close" || action === "close_all") {
        setBrowserPreviews((prev) => {
          if (action === "close_all") return {};
          const copy = { ...prev };
          delete copy[tabId];
          return copy;
        });
      }
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBrowserBusy(false);
    }
  };

  const previewBrowserTab = async (tabItem: BrowserTabInfo) => {
    if (previewingTabId !== null) return;
    setPreviewingTabId(tabItem.id);
    setError("");
    try {
      const capture = await api.browserScreenshot(tabItem.id);
      setBrowserPreviews((prev) => ({ ...prev, [tabItem.id]: capture }));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setPreviewingTabId(null);
    }
  };

  const activeSection = SETTINGS_SECTIONS.find((item) => item.id === tab) ?? SETTINGS_SECTIONS[0];
  const currentModel = models?.models.find((item) => item.current);
  const mcpServers = mcpState?.servers ?? [];
  const mcpBusy = refreshingServer !== null || capabilityBusy;
  const skills = skillState?.skills ?? [];
  const skillNotes = [...new Set(skillState?.notes)];
  const plugins = pluginState?.plugins ?? [];
  const browserTabs = browserState?.tabs ?? [];
  const skillFilter = skillQuery.trim().toLowerCase();
  const filteredSkills = skillFilter
    ? skills.filter(
        (item) =>
          item.name.toLowerCase().includes(skillFilter) ||
          (item.description ?? "").toLowerCase().includes(skillFilter) ||
          item.scope.toLowerCase().includes(skillFilter),
      )
    : skills;
  const panelBusy = busy || mcpBusy || runningSkillName !== null || browserBusy;

  return (
    <div
      className="control-overlay"
      data-open={open ? "true" : "false"}
      aria-hidden={!open}
    >
      <section className="control-panel" role="dialog" aria-modal="false" aria-label={activeSection.label}>
        <div className="control-panel__head">
          <div className="control-panel__heading">
            <span className="control-panel__title">{activeSection.label}</span>
            <span className="control-panel__subtitle">{activeSection.subtitle}</span>
          </div>

          <div className="control-panel__head-actions">
            {tab === "mcp" ? (
              <button
                type="button"
                className="icon-button"
                disabled={mcpBusy}
                onClick={() =>
                  void (mcpServers.some((s) => s.state === "connected")
                    ? refreshMCP("")
                    : loadMCP().catch((cause: unknown) =>
                        setError(cause instanceof Error ? cause.message : String(cause)),
                      ))
                }
                aria-label="刷新 MCP"
                title="刷新 MCP 状态与工具列表"
              >
                ↻
              </button>
            ) : null}
            {tab === "skills" ? (
              <button
                type="button"
                className="icon-button"
                disabled={runningSkillName !== null || capabilityBusy}
                onClick={() =>
                  void loadSkills().catch((cause: unknown) =>
                    setError(cause instanceof Error ? cause.message : String(cause)),
                  )
                }
                aria-label="刷新 Skill"
                title="重新扫描可用 Skill"
              >
                ↻
              </button>
            ) : null}
            {tab === "plugins" ? (
              <button
                type="button"
                className="icon-button"
                onClick={() =>
                  void loadPlugins().catch((cause: unknown) =>
                    setError(cause instanceof Error ? cause.message : String(cause)),
                  )
                }
                aria-label="刷新插件"
                title="重新扫描已安装插件"
              >
                ↻
              </button>
            ) : null}
            {tab === "browser" ? (
              <button
                type="button"
                className="icon-button"
                disabled={browserBusy}
                onClick={() =>
                  void loadBrowser().catch((cause: unknown) =>
                    setError(cause instanceof Error ? cause.message : String(cause)),
                  )
                }
                aria-label="刷新浏览器标签"
                title="刷新受控浏览器状态"
              >
                ↻
              </button>
            ) : null}
            <button
              type="button"
              className="icon-button"
              disabled={panelBusy}
              onClick={onClose}
              aria-label="返回对话"
              title="关闭设置（Esc）"
            >
              ✕
            </button>
          </div>
        </div>

        <div className="control-panel__body" ref={bodyRef}>
          <div className="control-panel__inner" data-tab={tab}>
            {error ? <div className="notice notice--error">{error}</div> : null}

            {tab === "models" ? (
              <div className="control-panel__sections">
                <div className="field">
                  <div className="field__head">
                    <div className="field__label">当前模型</div>
                    {modelConfig ? (
                      <button
                        type="button"
                        className="model-editor__toggle"
                        aria-expanded={editingModels}
                        disabled={busy}
                        onClick={() => setEditingModels((openEditor) => !openEditor)}
                      >
                        {editingModels ? "收起配置" : "编辑模型配置"}
                      </button>
                    ) : null}
                  </div>
                  <select
                    className="select"
                    value={models?.current ?? ""}
                    disabled={busy || !models}
                    onChange={(event) => void chooseModel(event.target.value)}
                  >
                    {(models?.models ?? []).map((option) => {
                      const value = `${option.provider}/${option.id}`;
                      return (
                        <option key={value} value={value}>
                          {option.name ? `${option.name} (${value})` : value}
                        </option>
                      );
                    })}
                  </select>
                  {models?.pending ? (
                    <div className="field__hint">
                      默认模型已更新为 {models.pending}，当前轮次继续使用 {models.current}，将在下一条消息生效
                    </div>
                  ) : null}
                  {currentModel?.contextWindow ? (
                    <div className="field__hint">
                      上下文窗口 {formatCount(currentModel.contextWindow)}
                      {currentModel.reasoning ? " · 支持推理" : ""}
                    </div>
                  ) : null}
                  {editingModels && modelConfig ? (
                    <ModelConfigEditor
                      ref={modelEditorRef}
                      initial={modelConfig}
                      busy={busy}
                      onReload={loadModelConfig}
                      onSave={saveModelConfig}
                      onDirtyChange={setModelEditorDirty}
                    />
                  ) : null}
                </div>

                {models && models.levels && models.levels.length > 0 ? (
                  <div className="field">
                    <div className="field__label">推理强度</div>
                    <div className="segmented">
                      {models.levels.map((level) => (
                        <button
                          key={level}
                          type="button"
                          aria-pressed={models.thinking === level}
                          disabled={busy}
                          onClick={() => void chooseThinking(level)}
                        >
                          {level}
                        </button>
                      ))}
                    </div>
                  </div>
                ) : null}
              </div>
            ) : null}

            {tab === "mcp" ? (
              <div className="control-panel__sections">
                {!mcpState ? <div className="rail__empty">读取 MCP 状态中…</div> : null}

                {mcpState && mcpServers.length === 0 ? (
                  <div className="mcp-empty">
                    <div className="mcp-empty__title">未配置 MCP 服务</div>
                    <p className="mcp-empty__desc">
                      可在配置文件中的 <code>mcpServers</code> 字段添加服务，控制面板查看与刷新不会打断任何正在运行的会话。
                    </p>
                    {mcpState.configPath ? (
                      <div className="mcp-empty__path">{mcpState.configPath}</div>
                    ) : null}
                  </div>
                ) : null}

                {mcpServers.length > 0 ? (
                  <div className="mcp-list">
                    {mcpServers.map((server) => {
                      const isOpen = expandedServers[server.name] ?? mcpServers.length <= 2;
                      const status = serverStatus(server);
                      const toolText =
                        server.declared !== server.tools.length && server.declared > 0
                          ? `${server.tools.length}/${server.declared} 个工具`
                          : `${server.tools.length} 个工具`;

                      return (
                        <section className="mcp-card" key={server.name} data-state={server.state}>
                          <div className="mcp-card__heading">
                            <button
                              type="button"
                              className="mcp-card__head"
                              aria-expanded={isOpen}
                              onClick={() => setExpandedServers((prev) => ({ ...prev, [server.name]: !isOpen }))}
                            >
                              <span className={`dot ${status.dotClass}`} />
                              <span className="mcp-card__title">
                                <span className="mcp-card__name">{server.name}</span>
                                {server.version ? (
                                  <span className="mcp-card__version">{server.version}</span>
                                ) : null}
                              </span>
                              <span className="mcp-card__badges">
                                {server.transport ? (
                                  <span className="mcp-badge">{server.transport}</span>
                                ) : null}
                                <span className="mcp-badge" data-tone={status.tone}>
                                  {status.label}
                                </span>
                              </span>
                              <span className="mcp-card__chevron" aria-hidden="true">
                                {isOpen ? "▾" : "▸"}
                              </span>
                            </button>
                            {mcpState?.canToggle ? (
                              <CapabilitySwitch
                                label={`启用 MCP ${server.name}`}
                                enabled={server.state !== "disabled"}
                                busy={mcpBusy || runningSkillName !== null}
                                onToggle={() => void changeCapability("mcp", server.name, server.state === "disabled")}
                              />
                            ) : null}
                          </div>

                          <div className="mcp-card__sub">
                            <span>{toolText}</span>
                            {server.state === "connected" ? (
                              <button
                                type="button"
                                className="mcp-card__refresh"
                                disabled={mcpBusy}
                                onClick={() => void refreshMCP(server.name)}
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
                            <div className="mcp-card__error">
                              工具列表刷新失败（保留旧列表）: {server.refreshError}
                            </div>
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
                                  {tool.description ? (
                                    <div className="mcp-tool__desc">{tool.description}</div>
                                  ) : null}
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
                ) : null}
              </div>
            ) : null}

            {tab === "skills" ? (
              <div className="control-panel__sections">
                {!skillState ? <div className="rail__empty">扫描可用 Skill 中…</div> : null}

                {skillState && skills.length === 0 ? (
                  <div className="mcp-empty">
                    <div className="mcp-empty__title">未发现可用 Skill</div>
                    <p className="mcp-empty__desc">
                      可在用户目录或当前项目下的 <code>skills</code> 目录中添加 <code>SKILL.md</code>，浏览器查看与预览不中断任何运行中的会话。
                    </p>
                    {skillState.roots.length > 0 ? (
                      <div className="skill-roots">
                        {skillState.roots.map((root) => (
                          <div className="skill-root" key={`${root.scope}:${root.dir}`}>
                            <span className="mcp-badge">{skillScopeLabel(root.scope)}</span>
                            <span className="skill-root__dir" title={root.dir}>
                              {root.dir}
                            </span>
                          </div>
                        ))}
                      </div>
                    ) : null}
                  </div>
                ) : null}

                {skillState && skills.length > 0 ? (
                  <>
                    <div className="skill-toolbar">
                      <input
                        type="search"
                        className="skill-search"
                        value={skillQuery}
                        onChange={(event) => setSkillQuery(event.target.value)}
                        placeholder={`搜索 ${skills.length} 个 Skill（名称、描述或来源）`}
                        aria-label="搜索 Skill"
                      />
                    </div>

                    {filteredSkills.length === 0 ? (
                      <div className="rail__empty">没有匹配「{skillQuery.trim()}」的 Skill</div>
                    ) : (
                      <div className="mcp-list">
                        {filteredSkills.map((skill) => {
                          const key = `${skill.scope}:${skill.path}`;
                          const isOpen = expandedSkills[key] ?? false;
                          const preview = skillPreviews[key];
                          const runningThis = runningSkillName === skill.name;

                          return (
                            <section className="mcp-card skill-card" key={key}>
                              <div className="mcp-card__heading">
                                <button
                                  type="button"
                                  className="mcp-card__head"
                                  aria-expanded={isOpen}
                                  onClick={() => void toggleSkillCard(key, skill.name)}
                                >
                                  <span className="mcp-card__title">
                                    <span className="mcp-card__name">{skill.name}</span>
                                  </span>
                                  <span className="mcp-card__badges">
                                    <span className="mcp-badge">{skillScopeLabel(skill.scope)}</span>
                                    {skill.disabled ? <span className="mcp-badge">已停用</span> : null}
                                  </span>
                                  <span className="mcp-card__chevron" aria-hidden="true">
                                    {isOpen ? "▾" : "▸"}
                                  </span>
                                </button>
                                {skillState.canToggle ? (
                                  <CapabilitySwitch
                                    label={`启用 Skill ${skill.name}`}
                                    enabled={!skill.disabled}
                                    busy={mcpBusy || runningSkillName !== null}
                                    onToggle={() => void changeCapability("skill", skill.name, Boolean(skill.disabled))}
                                  />
                                ) : null}
                              </div>

                              {isOpen ? (
                                <div className="skill-card__details">
                                  {skill.description ? (
                                    <div className="skill-card__desc">{skill.description}</div>
                                  ) : null}
                                  <div className="mcp-card__sub">
                                    <span className="skill-card__path">{skill.path}</span>
                                    <div className="skill-card__actions">
                                      <button
                                        type="button"
                                        className="mcp-card__refresh skill-card__use"
                                        disabled={skill.disabled || !canRunSkill || runningSkillName !== null || busy || capabilityBusy}
                                        title={
                                          skill.disabled
                                            ? "请先启用此 Skill"
                                            : !canRunSkill
                                              ? "当前会话正在运行或已归档，请等待结束或切换到空闲会话"
                                              : `加载 ${skill.name} 并在当前会话发送`
                                        }
                                        onClick={() => void useSkill(skill)}
                                      >
                                        {runningThis ? "发送中…" : "在当前会话使用"}
                                      </button>
                                    </div>
                                  </div>
                                  <div className="skill-preview">
                                    {preview?.status === "loaded" ? (
                                      <div className="skill-preview__body">
                                        {preview.body.trim() ? <MarkdownBody text={preview.body} /> : "暂无正文"}
                                      </div>
                                    ) : preview?.status === "error" ? (
                                      <div className="mcp-tools__empty" role="status">
                                        {preview.message}。收起后重新展开可重试。
                                      </div>
                                    ) : (
                                      <div className="mcp-tools__empty" role="status">读取 SKILL.md 中…</div>
                                    )}
                                  </div>
                                </div>
                              ) : null}
                            </section>
                          );
                        })}
                      </div>
                    )}
                  </>
                ) : null}

                {skillNotes.length > 0 ? (
                  <details className="mcp-card">
                    <summary className="skill-notes__summary">扫描提示（{skillNotes.length} 条）</summary>
                    <ul className="skill-notes__list">
                      {skillNotes.map((note) => <li key={note}>{note}</li>)}
                    </ul>
                  </details>
                ) : null}
              </div>
            ) : null}

            {tab === "plugins" ? (
              <div className="control-panel__sections">
                {!pluginState ? <div className="rail__empty">扫描已安装插件中…</div> : null}

                {pluginState && pluginState.notes && pluginState.notes.length > 0 ? (
                  <div className="notice">{pluginState.notes.join("\n")}</div>
                ) : null}

                {pluginState && plugins.length === 0 ? (
                  <div className="mcp-empty">
                    <div className="mcp-empty__title">未安装插件</div>
                    <p className="mcp-empty__desc">
                      可在插件目录下创建子目录并放置 <code>plugin.json</code>（声明 <code>skills</code> 目录与 <code>mcpServers</code> 服务），浏览不中断运行中的会话。
                    </p>
                    {pluginState.dir ? (
                      <div className="mcp-empty__path">{pluginState.dir}</div>
                    ) : null}
                  </div>
                ) : null}

                {plugins.length > 0 ? (
                  <div className="mcp-list">
                    {plugins.map((plugin) => (
                      <section className="mcp-card plugin-card" key={plugin.dir || plugin.name}>
                        <div className="browser-card__head">
                          <span className="dot" />
                          <div className="browser-card__title-wrap">
                            <div className="browser-card__title">{plugin.name}</div>
                            <div className="browser-card__url" title={plugin.dir}>
                              {plugin.dir}
                            </div>
                          </div>
                          <div className="mcp-card__badges">
                            <span className="mcp-badge">{plugin.skillNames.length} 个 Skill</span>
                            <span className="mcp-badge">{plugin.servers.length} 个 MCP</span>
                          </div>
                        </div>

                        {plugin.description ? (
                          <div className="skill-card__desc">{plugin.description}</div>
                        ) : null}

                        {plugin.skillNames.length > 0 || plugin.servers.length > 0 ? (
                          <div className="plugin-card__items">
                            {plugin.skillNames.map((name) => (
                              <button
                                key={`skill:${name}`}
                                type="button"
                                className="mcp-card__refresh"
                                title={`前往 Skill 面板筛选 ${name}`}
                                onClick={() => {
                                  setSkillQuery(name);
                                  onTabChange("skills");
                                }}
                              >
                                Skill: {name}
                              </button>
                            ))}
                            {plugin.servers.map((serverName) => (
                              <button
                                key={`mcp:${serverName}`}
                                type="button"
                                className="mcp-card__refresh"
                                title="前往 MCP 面板查看服务状态"
                                onClick={() => onTabChange("mcp")}
                              >
                                MCP: {serverName}
                              </button>
                            ))}
                          </div>
                        ) : (
                          <div className="mcp-tools__empty">该插件当前会话未激活任何 Skill 或 MCP 服务。</div>
                        )}
                      </section>
                    ))}
                  </div>
                ) : null}
              </div>
            ) : null}

            {tab === "browser" ? (
              <div className="control-panel__sections">
                {!browserState ? <div className="rail__empty">读取受控浏览器状态中…</div> : null}

                {browserState && !browserState.available ? (
                  <div className="mcp-empty">
                    <div className="mcp-empty__title">未检测到 Chrome / Chromium 浏览器</div>
                    <p className="mcp-empty__desc">
                      请安装 Google Chrome / Chromium，或在环境变量设置 <code>ATLAS_BROWSER_BIN</code> 后重启。
                    </p>
                  </div>
                ) : null}

                {browserState && browserState.available ? (
                  <>
                    <form
                      className="browser-bar"
                      onSubmit={(event) => {
                        event.preventDefault();
                        void openBrowserAddress();
                      }}
                    >
                      <input
                        type="text"
                        className="skill-search"
                        value={browserAddress}
                        onChange={(event) => setBrowserAddress(event.target.value)}
                        placeholder="输入网址或搜索关键词，在受控 Chrome 窗口中打开…"
                        aria-label="在受控浏览器打开网址"
                        disabled={browserBusy}
                      />
                      <button
                        type="submit"
                        className="button button--primary browser-bar__go"
                        disabled={browserBusy || !browserAddress.trim()}
                      >
                        {browserBusy ? "打开中…" : "打开"}
                      </button>
                    </form>

                    {browserTabs.length === 0 ? (
                      <div className="mcp-empty">
                        <div className="mcp-empty__title">受控浏览器待命</div>
                        <p className="mcp-empty__desc">
                          输入网址、点击对话中的网页链接，或由 Agent 调用 <code>browser_open</code> 时会自动拉起独立受控 Chrome 窗口。
                        </p>
                        {browserState.executable ? (
                          <div className="mcp-empty__path">{browserState.executable}</div>
                        ) : null}
                      </div>
                    ) : (
                      <div className="mcp-list">
                        {browserTabs.map((item) => {
                          const preview = browserPreviews[item.id];
                          const isPreviewing = previewingTabId === item.id;
                          return (
                            <section className="mcp-card browser-card" key={item.id} data-active={item.active ? "true" : undefined}>
                              <div className="browser-card__head">
                                <span className={`dot ${item.paused ? "dot--warn" : ""}`} />
                                <div className="browser-card__title-wrap">
                                  <div className="browser-card__title">{item.title || "新标签页"}</div>
                                  <div className="browser-card__url" title={item.url}>
                                    {item.url}
                                  </div>
                                </div>
                                <div className="mcp-card__badges">
                                  {item.loading ? <span className="mcp-badge">加载中</span> : null}
                                  <span
                                    className="mcp-badge"
                                    data-tone={item.paused ? "warn" : item.control === "agent" ? "ok" : undefined}
                                  >
                                    {item.paused ? "已接管(暂停Agent)" : item.control === "agent" ? "Agent" : "用户"}
                                  </span>
                                </div>
                              </div>

                              <div className="browser-card__actions">
                                <button
                                  type="button"
                                  className="mcp-card__refresh"
                                  disabled={browserBusy}
                                  onClick={() => void runBrowserAction(item.id, "activate")}
                                >
                                  切到窗口
                                </button>
                                <button
                                  type="button"
                                  className="mcp-card__refresh"
                                  disabled={browserBusy || !item.canGoBack}
                                  onClick={() => void runBrowserAction(item.id, "back")}
                                >
                                  后退
                                </button>
                                <button
                                  type="button"
                                  className="mcp-card__refresh"
                                  disabled={browserBusy || !item.canGoForward}
                                  onClick={() => void runBrowserAction(item.id, "forward")}
                                >
                                  前进
                                </button>
                                <button
                                  type="button"
                                  className="mcp-card__refresh"
                                  disabled={browserBusy}
                                  onClick={() => void runBrowserAction(item.id, "reload")}
                                >
                                  刷新
                                </button>
                                <button
                                  type="button"
                                  className="mcp-card__refresh"
                                  disabled={isPreviewing}
                                  onClick={() => void previewBrowserTab(item)}
                                >
                                  {isPreviewing ? "截图中…" : "画面预览"}
                                </button>
                                {item.paused ? (
                                  <button
                                    type="button"
                                    className="mcp-card__refresh skill-card__use"
                                    disabled={browserBusy}
                                    onClick={() => void runBrowserAction(item.id, "resume")}
                                  >
                                    交回 Agent
                                  </button>
                                ) : (
                                  <button
                                    type="button"
                                    className="mcp-card__refresh"
                                    disabled={browserBusy}
                                    onClick={() => void runBrowserAction(item.id, "takeover")}
                                  >
                                    接管
                                  </button>
                                )}
                                <button
                                  type="button"
                                  className="mcp-card__refresh"
                                  disabled={browserBusy}
                                  onClick={() => void runBrowserAction(item.id, "close")}
                                >
                                  关闭
                                </button>
                              </div>

                              {preview ? (
                                <div className="browser-preview">
                                  <img
                                    className="browser-preview__img"
                                    src={`data:${preview.mime};base64,${preview.base64}`}
                                    alt={item.title || item.url}
                                  />
                                </div>
                              ) : null}
                            </section>
                          );
                        })}
                      </div>
                    )}
                  </>
                ) : null}
              </div>
            ) : null}

            {tab === "devices" ? (
              <DevicesPanel
                data={deviceList}
                onDataChange={setDeviceList}
                onError={setError}
                onDevicesChanged={onDevicesChanged}
              />
            ) : null}

            {tab === "remote" ? (
              <RemotePanel
                status={remoteStatus}
                onStatusChange={setRemoteStatus}
                onError={setError}
                onOpenSession={onOpenSession}
              />
            ) : null}

            {tab === "schedules" ? (
              <SchedulePanel
                data={scheduleList}
                models={models}
                onDataChange={setScheduleList}
                onError={setError}
                onOpenSession={onOpenSession}
              />
            ) : null}

            {tab === "usage" ? <UsagePanel /> : null}

            {tab === "general" ? (
              <div className="control-panel__sections">
                {permission ? (
                  <div className="field">
                    <div className="field__label">权限</div>
                    <select
                      className="select"
                      value={permission.mode}
                      disabled={busy}
                      onChange={(event) => void choosePermission(event.target.value)}
                    >
                      {permission.modes.map((option) => (
                        <option key={option.value} value={option.value}>
                          {option.label}
                        </option>
                      ))}
                    </select>
                    <div className="field__hint">
                      {permission.modes.find((option) => option.value === permission.mode)?.note ?? ""}
                    </div>
                    <div className="field__hint">只影响本次运行，下次启动仍然读配置文件</div>
                  </div>
                ) : null}

                <div className="field">
                  <div className="field__label">外观</div>
                  <div className="segmented">
                    {(["system", "light", "dark"] as Theme[]).map((value) => (
                      <button
                        key={value}
                        type="button"
                        aria-pressed={theme === value}
                        onClick={() => onTheme(value)}
                      >
                        {value === "system" ? "跟随系统" : value === "light" ? "浅色" : "深色"}
                      </button>
                    ))}
                  </div>
                </div>

                {petVisible !== null ? (
                  <div className="field">
                    <div className="field__label">桌面宠物</div>
                    <div className="segmented">
                      <button
                        type="button"
                        aria-pressed={petVisible}
                        disabled={busy}
                        onClick={() => void choosePetVisible(true)}
                      >
                        显示
                      </button>
                      <button
                        type="button"
                        aria-pressed={!petVisible}
                        disabled={busy}
                        onClick={() => void choosePetVisible(false)}
                      >
                        隐藏
                      </button>
                    </div>
                    <div className="field__hint">
                      鲸鱼娘悬浮在所有窗口之上：有会话在跑时在电脑桌前工作，空闲时看电视；双击宠物回到主窗口，按住拖动可移动位置
                    </div>
                  </div>
                ) : null}
              </div>
            ) : null}
          </div>
        </div>

        {tab === "models" || tab === "general" ? (
          <div className="control-panel__foot">
            <button
              type="button"
              className="button button--primary"
              disabled={busy}
              onClick={() => void reloadConfigAndClose()}
            >
              {confirming
                ? "处理中…"
                : tab === "models" && editingModels && modelEditorDirty
                  ? "保存并重载"
                  : "确认并重载"}
            </button>
          </div>
        ) : tab === "browser" && browserTabs.length > 0 ? (
          <div className="control-panel__foot">
            <button
              type="button"
              className="button"
              disabled={browserBusy}
              onClick={() => void runBrowserAction("", "close_all")}
            >
              关闭全部标签
            </button>
          </div>
        ) : null}
      </section>
    </div>
  );
}

const SKILL_SCOPE_LABELS: Record<string, string> = {
  user: "用户",
  shared: "共享",
  plugin: "插件",
  project: "项目",
  "project-shared": "项目共享",
};

function skillMarkdownBody(text: string): string {
  return text.replace(/^\uFEFF/, "").replace(/^---[ \t]*\r?\n(?:[^\r\n]*\r?\n)*?---[ \t]*(?:\r?\n|$)/, "");
}

function skillScopeLabel(scope: string): string {
  return SKILL_SCOPE_LABELS[scope] || scope;
}

function CapabilitySwitch({ label, enabled, busy, onToggle }: {
  label: string;
  enabled: boolean;
  busy: boolean;
  onToggle: () => void;
}) {
  return (
    <button
      type="button"
      className="capability-switch"
      role="switch"
      aria-label={label}
      aria-checked={enabled}
      title={enabled ? "停用" : "启用"}
      disabled={busy}
      onClick={onToggle}
    />
  );
}

function serverStatus(server: MCPServerInfo): {
  label: string;
  tone?: "ok" | "warn" | "danger";
  dotClass: string;
} {
  if (server.state === "disabled") {
    return { label: "已停用", dotClass: "dot--disabled" };
  }
  if (server.state === "needs_login") {
    return { label: "需授权", tone: "warn", dotClass: "dot--warn" };
  }
  if (server.state === "failed") {
    return { label: "失败", tone: "danger", dotClass: "dot--danger" };
  }
  return { label: "已连接", tone: "ok", dotClass: "" };
}

function formatCount(value: number): string {
  if (!value) return "0";
  if (value >= 1000) return `${(value / 1000).toFixed(value >= 10000 ? 0 : 1)}k`;
  return String(value);
}
