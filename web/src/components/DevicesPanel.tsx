import { useState } from "react";
import { api, type DeviceInfo, type DeviceList } from "../api";
import { DeviceDialog } from "./DeviceDialog";

function deviceStatusMeta(status: DeviceInfo["status"]): {
  label: string;
  tone: "ok" | "warn" | "danger" | "";
  dot: string;
} {
  if (status === "online") {
    return { label: "已连接", tone: "ok", dot: "" };
  }
  if (status === "connecting") {
    return { label: "连接中", tone: "warn", dot: "dot--warn" };
  }
  if (status === "error") {
    return { label: "连接失败", tone: "danger", dot: "dot--danger" };
  }
  return { label: "未连接", tone: "", dot: "dot--muted" };
}

function targetAddress(dev: DeviceInfo): string {
  const hostPart = dev.user ? `${dev.user}@${dev.host}` : dev.host;
  return dev.port && dev.port !== 22 ? `${hostPart}:${dev.port}` : hostPart;
}

export function DevicesPanel({
  data,
  onDataChange,
  onError,
  onDevicesChanged,
}: {
  data: DeviceList | null;
  onDataChange: (next: DeviceList) => void;
  onError: (msg: string) => void;
  onDevicesChanged?: () => void;
}) {
  const [busyId, setBusyId] = useState<string | null>(null);
  const [note, setNote] = useState("");
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editing, setEditing] = useState<DeviceInfo | null>(null);
  const [expandedLogs, setExpandedLogs] = useState<Record<string, boolean>>({});

  const refresh = async () => {
    const next = await api.devices();
    onDataChange(next);
    onDevicesChanged?.();
  };

  const runDeviceAction = async (id: string, action: () => Promise<void>, okNote = "") => {
    setBusyId(id);
    setNote("");
    onError("");
    try {
      await action();
      await refresh();
      if (okNote) setNote(okNote);
    } catch (cause) {
      onError(cause instanceof Error ? cause.message : String(cause));
      try {
        onDataChange(await api.devices());
      } catch {
        // Ignore follow-up refresh error.
      }
    } finally {
      setBusyId(null);
    }
  };

  if (!data) {
    return <div className="panel-empty">正在加载远程设备列表…</div>;
  }

  return (
    <div className="control-panel__sections">
      <section className="panel-card">
        <div className="panel-card__head">
          <div>
            <div className="panel-card__title">
              <span>SSH 远程计算节点</span>
              <span className="badge">{data.devices.length} 台设备</span>
            </div>
            <div className="field__hint" style={{ marginTop: 4 }}>
              连接后通过本机 SSH 隧道自动拉起远端 <code>atlas -serve</code>，工具执行与文件读写全部在目标机器运行。
            </div>
          </div>
          <div className="skill-card__actions">
            <button
              type="button"
              className="button"
              disabled={busyId !== null}
              onClick={() => void refresh().catch((c) => onError(c instanceof Error ? c.message : String(c)))}
            >
              刷新
            </button>
            <button
              type="button"
              className="button button--primary"
              disabled={busyId !== null}
              onClick={() => {
                setEditing(null);
                setDialogOpen(true);
              }}
            >
              ＋ 添加设备
            </button>
          </div>
        </div>
      </section>

      {note ? <div className="notice">{note}</div> : null}

      {data.devices.length === 0 ? (
        <div className="panel-empty">
          还没有配置远程设备。点击上方「＋ 添加设备」添加 SSH 主机，或在「添加项目」弹窗中随时新增。
        </div>
      ) : (
        <div className="mcp-list">
          {data.devices.map((dev) => {
            const st = deviceStatusMeta(dev.status);
            const busy = busyId === dev.id;
            const logsOpen = Boolean(expandedLogs[dev.id]);
            return (
              <section className="panel-card" key={dev.id}>
                <div className="panel-card__head">
                  <div className="panel-card__title">
                    <span className={`dot ${st.dot}`} />
                    <span>{dev.name}</span>
                    <span className="badge" data-tone={st.tone || undefined}>
                      {st.label}
                    </span>
                    {dev.remoteOs ? (
                      <span className="badge">
                        {dev.remoteOs}/{dev.remoteArch}
                      </span>
                    ) : null}
                    {dev.remoteVersion ? <span className="badge">v{dev.remoteVersion}</span> : null}
                  </div>
                  <div className="skill-card__actions">
                    {dev.status === "online" ? (
                      <button
                        type="button"
                        className="button"
                        disabled={busyId !== null}
                        onClick={() =>
                          void runDeviceAction(
                            dev.id,
                            async () => {
                              await api.disconnectDevice(dev.id);
                            },
                            `已断开 ${dev.name}`,
                          )
                        }
                      >
                        {busy ? "处理中…" : "断开"}
                      </button>
                    ) : (
                      <button
                        type="button"
                        className="button button--primary"
                        disabled={busyId !== null}
                        onClick={() =>
                          void runDeviceAction(
                            dev.id,
                            async () => {
                              await api.connectDevice(dev.id);
                            },
                            `已连接到 ${dev.name}`,
                          )
                        }
                      >
                        {busy ? "连接中…" : "连接"}
                      </button>
                    )}
                    <button
                      type="button"
                      className="button"
                      disabled={busyId !== null}
                      title="将本机 ~/.atlas/config.json 模型与 API 密钥同步到该远程机器"
                      onClick={() =>
                        void runDeviceAction(
                          dev.id,
                          async () => {
                            await api.syncDeviceConfig(dev.id);
                          },
                          `已将本机模型配置同步到 ${dev.name}`,
                        )
                      }
                    >
                      同步模型配置
                    </button>
                    <button
                      type="button"
                      className="button"
                      disabled={busyId !== null}
                      onClick={() => {
                        setEditing(dev);
                        setDialogOpen(true);
                      }}
                    >
                      编辑
                    </button>
                    <button
                      type="button"
                      className="button"
                      disabled={busyId !== null}
                      onClick={() =>
                        void runDeviceAction(
                          dev.id,
                          async () => {
                            await api.deleteDevice(dev.id);
                          },
                          `已删除设备 ${dev.name}`,
                        )
                      }
                    >
                      删除
                    </button>
                  </div>
                </div>

                <div className="device-card__meta">
                  <span>
                    目标：<code>{targetAddress(dev)}</code>
                  </span>
                  {dev.remoteHome ? (
                    <span>
                      主目录：<code>{dev.remoteHome}</code>
                    </span>
                  ) : null}
                  {dev.remoteBin ? (
                    <span>
                      程序：<code>{dev.remoteBin}</code>
                    </span>
                  ) : null}
                  {dev.logs.length > 0 ? (
                    <button
                      type="button"
                      className="project-dialog__link"
                      onClick={() =>
                        setExpandedLogs((prev) => ({ ...prev, [dev.id]: !prev[dev.id] }))
                      }
                    >
                      {logsOpen ? "隐藏日志" : `查看日志 (${dev.logs.length})`}
                    </button>
                  ) : null}
                </div>

                {dev.error ? <div className="notice notice--error">{dev.error}</div> : null}

                {logsOpen && dev.logs.length > 0 ? (
                  <pre className="device-card__logs">{dev.logs.join("\n")}</pre>
                ) : null}
              </section>
            );
          })}
        </div>
      )}

      <DeviceDialog
        open={dialogOpen}
        initialDevice={editing}
        sshHosts={data.sshHosts ?? []}
        onSaved={async (saved, connectNow) => {
          setDialogOpen(false);
          setEditing(null);
          await refresh();
          setNote(connectNow ? `已保存并连接到 ${saved.name}` : `已保存设备 ${saved.name}`);
        }}
        onClose={() => {
          setDialogOpen(false);
          setEditing(null);
        }}
      />
    </div>
  );
}
