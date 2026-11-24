import { useCallback, useEffect, useRef, useState } from "react";
import {
  api,
  type DeviceInfo,
  type DeviceList,
  type DirBrowseResult,
  type DiscoveredProject,
} from "../api";
import { DeviceDialog } from "./DeviceDialog";

export interface ProjectDraft {
  path: string;
  name: string;
  deviceId?: string;
}

export interface FolderPickResult {
  path?: string;
  unsupported?: boolean;
}

/**
 * Modal dialog for adding a local or remote SSH project (modeled after zcode's
 * ProjectEditor + RemoteProjectList: name -> device -> folder/discover).
 */
export function ProjectDialog({
  open,
  submitting,
  error,
  onPickFolder,
  onConfirm,
  onClose,
}: {
  open: boolean;
  submitting: boolean;
  error: string;
  onPickFolder: () => Promise<FolderPickResult>;
  onConfirm: (draft: ProjectDraft) => Promise<void> | void;
  onClose: () => void;
}) {
  const [name, setName] = useState("");
  const [path, setPath] = useState("");
  const [deviceId, setDeviceId] = useState("local");
  const [deviceList, setDeviceList] = useState<DeviceList | null>(null);
  const [deviceDialogOpen, setDeviceDialogOpen] = useState(false);

  const [picking, setPicking] = useState(false);
  const [pickerUnsupported, setPickerUnsupported] = useState(false);
  const [manualEntry, setManualEntry] = useState(false);

  const [connecting, setConnecting] = useState(false);
  const [browseOpen, setBrowseOpen] = useState(false);
  const [browseLoading, setBrowseLoading] = useState(false);
  const [browseHidden, setBrowseHidden] = useState(false);
  const [browseResult, setBrowseResult] = useState<DirBrowseResult | null>(null);

  const [discoverOpen, setDiscoverOpen] = useState(false);
  const [discoverLoading, setDiscoverLoading] = useState(false);
  const [discovered, setDiscovered] = useState<DiscoveredProject[] | null>(null);

  const [localError, setLocalError] = useState("");

  const nameInputRef = useRef<HTMLInputElement>(null);
  const pathInputRef = useRef<HTMLInputElement>(null);
  const openEpoch = useRef(0);
  const composing = useRef(false);
  const justComposed = useRef(false);
  const suppressSubmit = useRef(false);

  const busy = submitting || picking || connecting;
  const isRemote = deviceId !== "local";
  const selectedDevice = (deviceList?.devices ?? []).find((d) => d.id === deviceId) ?? null;

  const refreshDevices = useCallback(async () => {
    try {
      const list = await api.devices();
      setDeviceList(list);
    } catch {
      setDeviceList({ sshHosts: [], devices: [] });
    }
  }, []);

  useEffect(() => {
    if (!open) {
      openEpoch.current++;
      return;
    }
    const epoch = ++openEpoch.current;
    setName("");
    setPath("");
    setDeviceId("local");
    setDeviceDialogOpen(false);
    setPicking(false);
    setPickerUnsupported(false);
    setManualEntry(false);
    setConnecting(false);
    setBrowseOpen(false);
    setBrowseLoading(false);
    setBrowseHidden(false);
    setBrowseResult(null);
    setDiscoverOpen(false);
    setDiscoverLoading(false);
    setDiscovered(null);
    setLocalError("");

    void refreshDevices();

    const frame = window.requestAnimationFrame(() => {
      if (openEpoch.current !== epoch) return;
      nameInputRef.current?.focus();
    });
    return () => {
      window.cancelAnimationFrame(frame);
    };
  }, [open, refreshDevices]);

  useEffect(() => {
    if (!open || deviceDialogOpen) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !busy) {
        onClose();
      }
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [open, deviceDialogOpen, busy, onClose]);

  const loadRemoteBrowse = useCallback(
    async (targetDeviceId: string, targetPath: string, includeHidden: boolean) => {
      const epoch = openEpoch.current;
      setBrowseLoading(true);
      setLocalError("");
      try {
        const res = await api.browseDirs(targetDeviceId, targetPath, includeHidden);
        if (openEpoch.current !== epoch) return;
        setBrowseResult(res);
        setBrowseOpen(true);
        await refreshDevices();
      } catch (cause) {
        if (openEpoch.current !== epoch) return;
        setLocalError(cause instanceof Error ? cause.message : String(cause));
      } finally {
        if (openEpoch.current === epoch) setBrowseLoading(false);
      }
    },
    [refreshDevices],
  );

  const loadRemoteDiscover = useCallback(
    async (targetDeviceId: string) => {
      const epoch = openEpoch.current;
      setDiscoverLoading(true);
      setLocalError("");
      try {
        const res = await api.discoverProjects(targetDeviceId);
        if (openEpoch.current !== epoch) return;
        setDiscovered(res.projects ?? []);
        await refreshDevices();
      } catch (cause) {
        if (openEpoch.current !== epoch) return;
        setLocalError(cause instanceof Error ? cause.message : String(cause));
      } finally {
        if (openEpoch.current === epoch) setDiscoverLoading(false);
      }
    },
    [refreshDevices],
  );

  const switchDevice = (nextDeviceId: string) => {
    setDeviceId(nextDeviceId);
    setPath("");
    setManualEntry(false);
    setBrowseOpen(false);
    setBrowseResult(null);
    setDiscoverOpen(false);
    setDiscovered(null);
    setLocalError("");
  };

  if (!open) return null;

  const displayError = localError || error;
  const trimmedPath = path.trim();
  const folderName = folderLeaf(trimmedPath);

  const handlePickFolder = async () => {
    if (busy || isRemote) return;
    const epoch = openEpoch.current;
    setPicking(true);
    setLocalError("");
    try {
      const result = await onPickFolder();
      if (openEpoch.current !== epoch) return;
      if (result.unsupported) {
        setPickerUnsupported(true);
        setManualEntry(true);
        window.requestAnimationFrame(() => {
          if (openEpoch.current === epoch) pathInputRef.current?.focus();
        });
        return;
      }
      if (result.path) {
        setPath(result.path);
        setManualEntry(false);
      }
    } catch (cause) {
      if (openEpoch.current !== epoch) return;
      setLocalError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      if (openEpoch.current === epoch) setPicking(false);
    }
  };

  const handleConnectSelected = async () => {
    if (!selectedDevice || busy) return;
    const epoch = openEpoch.current;
    setConnecting(true);
    setLocalError("");
    try {
      await api.connectDevice(selectedDevice.id);
      if (openEpoch.current !== epoch) return;
      await refreshDevices();
    } catch (cause) {
      if (openEpoch.current !== epoch) return;
      setLocalError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      if (openEpoch.current === epoch) setConnecting(false);
    }
  };

  const imeGuardProps = {
    onCompositionStart: () => {
      composing.current = true;
    },
    onCompositionEnd: () => {
      composing.current = false;
      justComposed.current = true;
      window.setTimeout(() => {
        justComposed.current = false;
      }, 0);
    },
    onKeyDown: (event: React.KeyboardEvent<HTMLInputElement>) => {
      if (event.key !== "Enter") return;
      const native = event.nativeEvent;
      if (composing.current || justComposed.current || native.isComposing || native.keyCode === 229) {
        justComposed.current = false;
        suppressSubmit.current = true;
        window.setTimeout(() => {
          suppressSubmit.current = false;
        }, 0);
      }
    },
  };

  return (
    <>
      <div
        className="sheet-backdrop"
        role="presentation"
        onMouseDown={(event) => {
          if (event.target === event.currentTarget && !busy && !deviceDialogOpen) onClose();
        }}
      >
        <div
          className="sheet sheet--narrow project-dialog"
          role="dialog"
          aria-modal="true"
          aria-label="添加项目"
        >
          <div className="sheet__head">
            <span className="sheet__title">添加项目</span>
            <button
              type="button"
              className="icon-button"
              onClick={onClose}
              disabled={busy}
              aria-label="关闭"
            >
              ✕
            </button>
          </div>

          <form
            className="sheet__body"
            onSubmit={(event) => {
              event.preventDefault();
              if (suppressSubmit.current) {
                suppressSubmit.current = false;
                return;
              }
              if (busy) return;
              if (!trimmedPath) {
                setLocalError("请选择或输入项目目录。");
                return;
              }
              setLocalError("");
              void onConfirm({
                path: trimmedPath,
                name: name.trim(),
                ...(isRemote ? { deviceId } : {}),
              });
            }}
          >
            {displayError ? <div className="notice notice--error project-dialog__error">{displayError}</div> : null}

            <div className="field">
              <label className="field__label" htmlFor="project-name-input">
                项目名称
              </label>
              <input
                id="project-name-input"
                ref={nameInputRef}
                className="field__input"
                value={name}
                placeholder={folderName || "新项目"}
                disabled={busy}
                onChange={(event) => setName(event.target.value)}
                {...imeGuardProps}
              />
              <div className="field__hint">留空则默认使用所选文件夹名称。</div>
            </div>

            <div className="field">
              <div className="project-dialog__field-head">
                <label className="field__label" htmlFor="project-device-select">
                  设备
                </label>
                <button
                  type="button"
                  className="project-dialog__link"
                  disabled={busy}
                  onClick={() => setDeviceDialogOpen(true)}
                >
                  ＋ 添加远程设备…
                </button>
              </div>
              <div className="project-dialog__device-wrap">
                <span className="project-dialog__device-icon" aria-hidden="true">
                  {isRemote ? <ServerIcon /> : <MonitorIcon />}
                </span>
                <select
                  id="project-device-select"
                  className="select project-dialog__device-select"
                  value={deviceId}
                  disabled={busy}
                  onChange={(event) => switchDevice(event.target.value)}
                >
                  <option value="local">此电脑（本地）</option>
                  {(deviceList?.devices ?? []).map((dev) => (
                    <option key={dev.id} value={dev.id}>
                      {dev.name} ({deviceStateText(dev)})
                    </option>
                  ))}
                </select>
              </div>
              {isRemote && selectedDevice ? (
                <div className="project-dialog__device-status">
                  <span>
                    目标：<code>{selectedDevice.user ? `${selectedDevice.user}@${selectedDevice.host}` : selectedDevice.host}</code>
                    {selectedDevice.remoteOs ? ` · ${selectedDevice.remoteOs}/${selectedDevice.remoteArch}` : ""}
                  </span>
                  {selectedDevice.status !== "online" ? (
                    <button
                      type="button"
                      className="button project-dialog__mini-btn"
                      disabled={busy}
                      onClick={() => void handleConnectSelected()}
                    >
                      {connecting ? "连接中…" : "立即连接"}
                    </button>
                  ) : (
                    <span className="badge" data-tone="ok">
                      已连接
                    </span>
                  )}
                </div>
              ) : (
                <div className="field__hint">选择本地工作区，或选择已配置的 SSH 远程主机执行对话与命令。</div>
              )}
            </div>

            {isRemote ? (
              <div className="project-dialog__discover">
                <button
                  type="button"
                  className="project-dialog__discover-toggle"
                  disabled={busy}
                  onClick={() => {
                    const next = !discoverOpen;
                    setDiscoverOpen(next);
                    if (next && discovered === null && !discoverLoading) {
                      void loadRemoteDiscover(deviceId);
                    }
                  }}
                >
                  <span>这台设备上已有项目？添加到侧栏</span>
                  <span>{discoverOpen ? "▾" : "▸"}</span>
                </button>
                {discoverOpen ? (
                  <div className="project-dialog__discover-body">
                    {discoverLoading ? (
                      <div className="project-dialog__discover-hint">正在连接并读取远端已有项目…</div>
                    ) : !discovered || discovered.length === 0 ? (
                      <div className="project-dialog__discover-hint">未在这台设备上扫描到已记录的历史项目。</div>
                    ) : (
                      <div className="project-dialog__discover-list">
                        {discovered.map((item) => (
                          <div className="project-dialog__discover-row" key={item.path}>
                            <div className="project-dialog__discover-meta">
                              <div className="project-dialog__discover-name">
                                {item.name || folderLeaf(item.path)}
                                {item.sessionCount ? (
                                  <span className="project-dialog__count-pill">{item.sessionCount} 会话</span>
                                ) : null}
                              </div>
                              <div className="project-dialog__discover-path" title={item.path}>
                                {item.path}
                              </div>
                            </div>
                            {item.added ? (
                              <span className="badge">已添加</span>
                            ) : (
                              <button
                                type="button"
                                className="button button--primary project-dialog__mini-btn"
                                disabled={busy}
                                onClick={() => {
                                  void onConfirm({
                                    path: item.path,
                                    name: name.trim() || item.name || folderLeaf(item.path),
                                    deviceId,
                                  });
                                }}
                              >
                                添加
                              </button>
                            )}
                          </div>
                        ))}
                      </div>
                    )}
                  </div>
                ) : null}
              </div>
            ) : null}

            <div className="field">
              <div className="project-dialog__field-head">
                <label className="field__label" htmlFor={manualEntry ? "project-path-input" : undefined}>
                  项目目录
                </label>
                {!manualEntry ? (
                  <button
                    type="button"
                    className="project-dialog__link"
                    disabled={busy}
                    onClick={() => {
                      setLocalError("");
                      setManualEntry(true);
                      setBrowseOpen(false);
                      window.requestAnimationFrame(() => pathInputRef.current?.focus());
                    }}
                  >
                    手动输入路径
                  </button>
                ) : (
                  <button
                    type="button"
                    className="project-dialog__link"
                    disabled={busy}
                    onClick={() => {
                      setLocalError("");
                      setManualEntry(false);
                    }}
                  >
                    {isRemote ? "返回远端浏览" : "返回卡片视图"}
                  </button>
                )}
              </div>

              {manualEntry ? (
                <>
                  <div className="project-dialog__path-row">
                    <input
                      id="project-path-input"
                      ref={pathInputRef}
                      className="field__input"
                      value={path}
                      placeholder="/绝对路径 或 ~/目录"
                      disabled={busy}
                      onChange={(event) => {
                        setPath(event.target.value);
                        if (localError) setLocalError("");
                      }}
                      {...imeGuardProps}
                    />
                    {isRemote ? (
                      <button
                        type="button"
                        className="button"
                        disabled={busy || browseLoading}
                        onClick={() => {
                          setManualEntry(false);
                          void loadRemoteBrowse(deviceId, trimmedPath || "", browseHidden);
                        }}
                      >
                        {browseLoading ? "读取中…" : "浏览…"}
                      </button>
                    ) : pickerUnsupported ? null : (
                      <button
                        type="button"
                        className="button"
                        disabled={busy}
                        onClick={() => void handlePickFolder()}
                      >
                        {picking ? "选择中…" : "浏览…"}
                      </button>
                    )}
                  </div>
                  <div className="field__hint">
                    {isRemote
                      ? "输入远程机器上的 POSIX 目录路径（支持 / 绝对路径或 ~/ 开头），或点击「浏览…」在线挑选。"
                      : pickerUnsupported
                        ? "当前运行环境无系统目录选择器，请手动输入目录路径（支持 ~）。"
                        : "支持绝对路径或 ~/ 开头路径；也可点击「浏览…」打开系统目录选择器。"}
                  </div>
                </>
              ) : isRemote ? (
                browseOpen ? (
                  <div className="project-dialog__browser">
                    <div className="project-dialog__browser-bar">
                      <button
                        type="button"
                        className="button project-dialog__mini-btn"
                        disabled={browseLoading}
                        onClick={() => void loadRemoteBrowse(deviceId, "", browseHidden)}
                      >
                        主目录
                      </button>
                      <button
                        type="button"
                        className="button project-dialog__mini-btn"
                        disabled={browseLoading || !browseResult?.parent}
                        onClick={() =>
                          browseResult?.parent &&
                          void loadRemoteBrowse(deviceId, browseResult.parent, browseHidden)
                        }
                      >
                        上级
                      </button>
                      <span className="project-dialog__browser-path" title={browseResult?.path}>
                        {browseResult?.path || "读取中…"}
                      </span>
                      <label className="project-dialog__hidden-toggle">
                        <input
                          type="checkbox"
                          checked={browseHidden}
                          onChange={(e) => {
                            const next = e.target.checked;
                            setBrowseHidden(next);
                            void loadRemoteBrowse(deviceId, browseResult?.path || "", next);
                          }}
                        />
                        隐藏
                      </label>
                    </div>

                    <div className="project-dialog__browser-list">
                      {browseLoading ? (
                        <div className="project-dialog__browser-empty">正在读取远程目录…</div>
                      ) : !browseResult || browseResult.entries.length === 0 ? (
                        <div className="project-dialog__browser-empty">当前目录没有子文件夹</div>
                      ) : (
                        browseResult.entries.map((entry) => (
                          <button
                            key={entry.path}
                            type="button"
                            className="project-dialog__browser-item"
                            onClick={() => void loadRemoteBrowse(deviceId, entry.path, browseHidden)}
                          >
                            <FolderIcon />
                            <span>{entry.name}</span>
                          </button>
                        ))
                      )}
                    </div>

                    <div className="project-dialog__browser-foot">
                      <button
                        type="button"
                        className="button"
                        onClick={() => setBrowseOpen(false)}
                      >
                        收起
                      </button>
                      <button
                        type="button"
                        className="button button--primary"
                        disabled={!browseResult?.path}
                        onClick={() => {
                          if (!browseResult?.path) return;
                          setPath(browseResult.path);
                          setBrowseOpen(false);
                        }}
                      >
                        选择此目录
                      </button>
                    </div>
                  </div>
                ) : trimmedPath ? (
                  <div className="project-dialog__folder-card">
                    <span className="project-dialog__folder-icon" aria-hidden="true">
                      <FolderIcon />
                    </span>
                    <div className="project-dialog__folder-meta">
                      <div className="project-dialog__folder-name">{folderName || trimmedPath}</div>
                      <div className="project-dialog__folder-path" title={trimmedPath}>
                        {trimmedPath}
                      </div>
                    </div>
                    <div className="project-dialog__folder-actions">
                      <button
                        type="button"
                        className="button project-dialog__mini-btn"
                        disabled={busy || browseLoading}
                        onClick={() => void loadRemoteBrowse(deviceId, trimmedPath, browseHidden)}
                      >
                        更换
                      </button>
                      <button
                        type="button"
                        className="icon-button"
                        disabled={busy}
                        title="清除所选目录"
                        aria-label="清除所选目录"
                        onClick={() => {
                          setPath("");
                          setLocalError("");
                        }}
                      >
                        ✕
                      </button>
                    </div>
                  </div>
                ) : (
                  <button
                    type="button"
                    className="project-dialog__dropzone"
                    disabled={busy || browseLoading}
                    onClick={() => void loadRemoteBrowse(deviceId, "", browseHidden)}
                  >
                    <span className="project-dialog__dropzone-icon" aria-hidden="true">
                      <FolderPlusIcon />
                    </span>
                    <span className="project-dialog__dropzone-title">
                      {browseLoading ? "正在读取远程目录…" : "浏览远程目录"}
                    </span>
                    <span className="project-dialog__dropzone-hint">
                      在 {selectedDevice?.name || "远程设备"} 上挑选或浏览工作区文件夹
                    </span>
                  </button>
                )
              ) : trimmedPath ? (
                <div className="project-dialog__folder-card">
                  <span className="project-dialog__folder-icon" aria-hidden="true">
                    <FolderIcon />
                  </span>
                  <div className="project-dialog__folder-meta">
                    <div className="project-dialog__folder-name">{folderName || trimmedPath}</div>
                    <div className="project-dialog__folder-path" title={trimmedPath}>
                      {trimmedPath}
                    </div>
                  </div>
                  <div className="project-dialog__folder-actions">
                    {pickerUnsupported ? null : (
                      <button
                        type="button"
                        className="button project-dialog__mini-btn"
                        disabled={busy}
                        onClick={() => void handlePickFolder()}
                      >
                        {picking ? "选择中…" : "更换"}
                      </button>
                    )}
                    <button
                      type="button"
                      className="icon-button"
                      disabled={busy}
                      title="清除所选目录"
                      aria-label="清除所选目录"
                      onClick={() => {
                        setPath("");
                        setLocalError("");
                      }}
                    >
                      ✕
                    </button>
                  </div>
                </div>
              ) : (
                <button
                  type="button"
                  className="project-dialog__dropzone"
                  disabled={busy}
                  onClick={() => void handlePickFolder()}
                >
                  <span className="project-dialog__dropzone-icon" aria-hidden="true">
                    <FolderPlusIcon />
                  </span>
                  <span className="project-dialog__dropzone-title">
                    {picking ? "正在打开目录选择器…" : "选择文件夹"}
                  </span>
                  <span className="project-dialog__dropzone-hint">
                    Atlas 将读取并关联该目录下的已有会话
                  </span>
                </button>
              )}
            </div>

            <div className="sheet__actions">
              <button type="button" className="button" onClick={onClose} disabled={busy}>
                取消
              </button>
              <button
                type="submit"
                className="button button--primary"
                disabled={busy || !trimmedPath}
              >
                {submitting ? "添加中…" : "添加项目"}
              </button>
            </div>
          </form>
        </div>
      </div>

      <DeviceDialog
        open={deviceDialogOpen}
        initialDevice={null}
        sshHosts={deviceList?.sshHosts ?? []}
        onSaved={async (saved) => {
          setDeviceDialogOpen(false);
          await refreshDevices();
          switchDevice(saved.id);
        }}
        onClose={() => setDeviceDialogOpen(false)}
      />
    </>
  );
}

function deviceStateText(dev: DeviceInfo): string {
  if (dev.status === "online") return "在线";
  if (dev.status === "connecting") return "连接中";
  if (dev.status === "error") return "失败";
  return "未连接";
}

function folderLeaf(raw: string): string {
  const trimmed = raw.trim();
  if (!trimmed) return "";
  if (trimmed === "/" || trimmed === "~") return trimmed;
  const cleaned = trimmed.replace(/[\\/]+$/, "");
  if (!cleaned) return "/";
  const parts = cleaned.split(/[\\/]/);
  return parts[parts.length - 1] ?? cleaned;
}

function MonitorIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round">
      <rect x="2" y="2.5" width="12" height="8.5" rx="1.4" />
      <path d="M5.5 13.5h5M8 11v2.5" />
    </svg>
  );
}

function ServerIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round">
      <rect x="2" y="2.5" width="12" height="4.5" rx="1.2" />
      <rect x="2" y="9" width="12" height="4.5" rx="1.2" />
      <circle cx="4.5" cy="4.75" r="0.6" fill="currentColor" />
      <circle cx="4.5" cy="11.25" r="0.6" fill="currentColor" />
    </svg>
  );
}

function FolderIcon() {
  return (
    <svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round">
      <path d="M1.75 3.75A1.25 1.25 0 0 1 3 2.5h3.1l1.4 1.5H13A1.25 1.25 0 0 1 14.25 5.25v6.75A1.25 1.25 0 0 1 13 13.25H3A1.25 1.25 0 0 1 1.75 12V3.75Z" />
    </svg>
  );
}

function FolderPlusIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round">
      <path d="M1.75 3.75A1.25 1.25 0 0 1 3 2.5h3.1l1.4 1.5H13A1.25 1.25 0 0 1 14.25 5.25v6.75A1.25 1.25 0 0 1 13 13.25H3A1.25 1.25 0 0 1 1.75 12V3.75Z" />
      <path d="M8 6.75v3.5M6.25 8.5h3.5" />
    </svg>
  );
}
