import { useEffect, useState } from "react";
import { api, ApiError, type RemoteStatus } from "../api";

interface FeishuDraft {
  enabled: boolean;
  appId: string;
  appSecret: string;
  clearSecret: boolean;
  dmPolicy: string;
  groupPolicy: string;
  requireMention: boolean;
  allowBots: boolean;
  cwd: string;
  adminsText: string;
  allowedUsersText: string;
}

function draftFromStatus(st: RemoteStatus): FeishuDraft {
  const f = st.feishu;
  return {
    enabled: f.enabled,
    appId: f.appId,
    appSecret: "",
    clearSecret: false,
    dmPolicy: f.dmPolicy || "pairing",
    groupPolicy: f.groupPolicy || "allowlist",
    requireMention: f.requireMention,
    allowBots: f.allowBots,
    cwd: f.cwd ?? "",
    adminsText: (f.admins ?? []).join(", "),
    allowedUsersText: (f.allowedUsers ?? []).join(", "),
  };
}

function parseListInput(raw: string): string[] {
  return raw
    .split(/[\n,，\s]+/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function statusMeta(st: RemoteStatus | null): {
  label: string;
  tone: "ok" | "warn" | "danger" | "";
  dot: string;
} {
  if (!st || st.state === "stopped") {
    return { label: "已停止", tone: "", dot: "dot--muted" };
  }
  if (st.state === "error" || st.connection === "error") {
    return { label: "连接异常", tone: "danger", dot: "dot--danger" };
  }
  if (st.connection === "connected") {
    return { label: "已连接", tone: "ok", dot: "" };
  }
  if (st.connection === "reconnecting") {
    return { label: "重连中", tone: "warn", dot: "dot--warn" };
  }
  return { label: "连接中", tone: "warn", dot: "dot--warn" };
}

function formatStamp(iso?: string): string {
  if (!iso) return "";
  const dt = new Date(iso);
  if (Number.isNaN(dt.getTime())) return iso;
  return dt.toLocaleString("zh-CN", { hour12: false });
}

export function RemotePanel({
  status,
  onStatusChange,
  onError,
  onOpenSession,
}: {
  status: RemoteStatus | null;
  onStatusChange: (next: RemoteStatus) => void;
  onError: (message: string) => void;
  onOpenSession: (path: string) => Promise<void> | void;
}) {
  const [draft, setDraft] = useState<FeishuDraft | null>(() => (status ? draftFromStatus(status) : null));
  const [dirty, setDirty] = useState(false);
  const [busy, setBusy] = useState(false);
  const [pairCode, setPairCode] = useState("");
  const [manualUser, setManualUser] = useState("");
  const [savedNote, setSavedNote] = useState("");

  useEffect(() => {
    if (!status) return;
    if (!dirty || !draft) {
      setDraft(draftFromStatus(status));
    }
  }, [status, dirty, draft]);

  useEffect(() => {
    if (!status || status.state !== "running") return;
    const timer = window.setInterval(() => {
      void api.remote().then(onStatusChange).catch(() => {});
    }, 3000);
    return () => window.clearInterval(timer);
  }, [status?.state, onStatusChange]);

  const runAction = async (fn: () => Promise<RemoteStatus>, note = "", resetDirty = false) => {
    setBusy(true);
    setSavedNote("");
    onError("");
    try {
      const next = await fn();
      if (resetDirty) {
        setDirty(false);
        setDraft(draftFromStatus(next));
      }
      onStatusChange(next);
      if (note) setSavedNote(note);
    } catch (cause) {
      onError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  };

  const updateDraft = (patch: Partial<FeishuDraft>) => {
    setDirty(true);
    setSavedNote("");
    setDraft((prev) => (prev ? { ...prev, ...patch } : prev));
  };

  const pickCwd = async () => {
    try {
      const chosen = await api.pickFolder();
      if (chosen.path) {
        updateDraft({ cwd: chosen.path });
      }
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 501) {
        onError("当前浏览器模式不支持系统文件夹选择器，请直接输入目录路径。");
        return;
      }
      onError(cause instanceof Error ? cause.message : String(cause));
    }
  };

  const saveConfig = async (startAfterSave: boolean) => {
    if (!draft) return;
    await runAction(
      () =>
        api.saveFeishu({
          enabled: draft.enabled,
          appId: draft.appId.trim(),
          appSecret: draft.appSecret.trim(),
          clearSecret: draft.clearSecret,
          dmPolicy: draft.dmPolicy,
          groupPolicy: draft.groupPolicy,
          requireMention: draft.requireMention,
          allowBots: draft.allowBots,
          cwd: draft.cwd.trim(),
          admins: parseListInput(draft.adminsText),
          allowedUsers: parseListInput(draft.allowedUsersText),
          startAfterSave,
        }),
      startAfterSave ? "已保存配置并启动飞书长连接" : "已保存飞书配置",
      true,
    );
  };

  if (!status || !draft) {
    return <div className="panel-empty">正在读取远程控制配置…</div>;
  }

  const meta = statusMeta(status);
  const running = status.state === "running";

  return (
    <div className="control-panel__sections">
      <section className="panel-card">
        <div className="panel-card__head">
          <div className="panel-card__title">
            <span className={`dot ${meta.dot}`} />
            <span>飞书机器人网关</span>
            <span className="badge" data-tone={meta.tone || undefined}>
              {meta.label}
            </span>
          </div>
          <div className="skill-card__actions">
            <button
              type="button"
              className="button"
              disabled={busy}
              onClick={() => void runAction(() => api.remote())}
            >
              刷新
            </button>
            {running ? (
              <button
                type="button"
                className="button"
                disabled={busy}
                onClick={() => void runAction(() => api.stopRemote(), "已停止飞书长连接")}
              >
                停止连接
              </button>
            ) : (
              <button
                type="button"
                className="button button--primary"
                disabled={busy}
                onClick={() => void runAction(() => api.startRemote(), "飞书远程控制已启动")}
              >
                启动连接
              </button>
            )}
          </div>
        </div>
        <div className="panel-card__meta">
          {status.startedAt ? <span>启动于 {formatStamp(status.startedAt)}</span> : <span>当前未建立 WebSocket 长连接</span>}
          {status.defaultCwd ? <span className="skill-card__path">默认目录 {status.defaultCwd}</span> : null}
        </div>
        {status.lastError ? <div className="panel-card__error">{status.lastError}</div> : null}
        {savedNote ? <div className="remote-note">{savedNote}</div> : null}
      </section>

      <section className="panel-card remote-form">
        <div className="panel-card__head">
          <div className="panel-card__title">
            <span>飞书应用凭据与权限策略</span>
          </div>
          <label className="remote-check">
            <input
              type="checkbox"
              checked={draft.enabled}
              disabled={busy}
              onChange={(event) => updateDraft({ enabled: event.target.checked })}
            />
            <span>应用启动时自动连接</span>
          </label>
        </div>

        <div className="remote-grid">
          <label className="field">
            <span className="field__label">App ID</span>
            <input
              type="text"
              className="input"
              value={draft.appId}
              placeholder="cli_xxxxxxxxxxxxxxxx"
              disabled={busy}
              onChange={(event) => updateDraft({ appId: event.target.value })}
            />
          </label>

          <div className="field">
            <div className="remote-label-row">
              <span className="field__label">App Secret</span>
              {status.feishu.hasSecret && !draft.clearSecret ? (
                <span className="badge" data-tone="ok">
                  {status.feishu.secretMasked || "已配置"}
                </span>
              ) : (
                <span className="badge">未配置</span>
              )}
            </div>
            <div className="remote-secret-row">
              <input
                type="password"
                className="input"
                value={draft.appSecret}
                placeholder={
                  status.feishu.hasSecret && !draft.clearSecret
                    ? "留空保持现有密钥；也可填新密钥或 env:VAR"
                    : "填入 App Secret 或 env:FEISHU_APP_SECRET"
                }
                disabled={busy}
                onChange={(event) => updateDraft({ appSecret: event.target.value, clearSecret: false })}
              />
              {status.feishu.hasSecret && !draft.clearSecret ? (
                <button
                  type="button"
                  className="button"
                  disabled={busy}
                  onClick={() => updateDraft({ appSecret: "", clearSecret: true })}
                >
                  清除
                </button>
              ) : null}
            </div>
            {status.feishu.envOverride ? (
              <div className="field__hint">当前检测到环境变量 ATLAS_FEISHU_*，启动时环境变量会优先覆盖文件配置</div>
            ) : null}
          </div>
        </div>

        <div className="remote-grid">
          <label className="field">
            <span className="field__label">私聊访问策略 (dmPolicy)</span>
            <select
              className="select"
              value={draft.dmPolicy}
              disabled={busy}
              onChange={(event) => updateDraft({ dmPolicy: event.target.value })}
            >
              <option value="pairing">pairing — 首次私聊发放 6 位配对码审批</option>
              <option value="allowlist">allowlist — 仅允许白名单用户</option>
              <option value="open">open — 允许所有可读到机器人的私聊</option>
              <option value="disabled">disabled — 禁用私聊</option>
            </select>
          </label>

          <label className="field">
            <span className="field__label">群聊访问策略 (groupPolicy)</span>
            <select
              className="select"
              value={draft.groupPolicy}
              disabled={busy}
              onChange={(event) => updateDraft({ groupPolicy: event.target.value })}
            >
              <option value="allowlist">allowlist — 群内仅响应白名单发送者</option>
              <option value="open">open — 群内所有人均可触发</option>
              <option value="admin_only">admin_only — 仅管理员可触发</option>
              <option value="disabled">disabled — 禁用群聊</option>
            </select>
          </label>
        </div>

        <div className="field">
          <span className="field__label">新会话默认工作目录 (cwd)</span>
          <div className="remote-secret-row">
            <input
              type="text"
              className="input"
              value={draft.cwd}
              placeholder={status.defaultCwd ? `留空使用默认目录 (${status.defaultCwd})` : "留空使用系统启动目录"}
              disabled={busy}
              onChange={(event) => updateDraft({ cwd: event.target.value })}
            />
            <button type="button" className="button" disabled={busy} onClick={() => void pickCwd()}>
              选择目录
            </button>
          </div>
        </div>

        <div className="remote-checks">
          <label className="remote-check">
            <input
              type="checkbox"
              checked={draft.requireMention}
              disabled={busy}
              onChange={(event) => updateDraft({ requireMention: event.target.checked })}
            />
            <span>群聊需要 @ 机器人才响应</span>
          </label>
          <label className="remote-check">
            <input
              type="checkbox"
              checked={draft.allowBots}
              disabled={busy}
              onChange={(event) => updateDraft({ allowBots: event.target.checked })}
            />
            <span>允许接收其他机器人的消息</span>
          </label>
        </div>

        <div className="remote-grid">
          <label className="field">
            <span className="field__label">管理员用户 ID（逗号分隔，可用 /cwd）</span>
            <input
              type="text"
              className="input"
              value={draft.adminsText}
              placeholder="ou_xxx, ou_yyy"
              disabled={busy}
              onChange={(event) => updateDraft({ adminsText: event.target.value })}
            />
          </label>

          <label className="field">
            <span className="field__label">静态白名单用户 ID（逗号分隔）</span>
            <input
              type="text"
              className="input"
              value={draft.allowedUsersText}
              placeholder="ou_xxx, ou_yyy"
              disabled={busy}
              onChange={(event) => updateDraft({ allowedUsersText: event.target.value })}
            />
          </label>
        </div>

        <div className="remote-form__actions">
          <button
            type="button"
            className="button"
            disabled={busy}
            onClick={() => void saveConfig(false)}
          >
            保存配置
          </button>
          <button
            type="button"
            className="button button--primary"
            disabled={busy}
            onClick={() => void saveConfig(true)}
          >
            {running ? "保存并重连" : "保存并启动"}
          </button>
        </div>
      </section>

      <section className="panel-card">
        <div className="panel-card__head">
          <div className="panel-card__title">
            <span>配对审批与运行时白名单</span>
          </div>
        </div>

        <div className="remote-inline-actions">
          <div className="remote-secret-row">
            <input
              type="text"
              className="input"
              value={pairCode}
              placeholder="输入 6 位配对码批准接入"
              disabled={busy}
              onChange={(event) => setPairCode(event.target.value)}
            />
            <button
              type="button"
              className="button"
              disabled={busy || !pairCode.trim()}
              onClick={() =>
                void runAction(async () => {
                  const next = await api.approvePairing(pairCode.trim());
                  setPairCode("");
                  return next;
                }, "配对码已批准并加入白名单")
              }
            >
              批准配对码
            </button>
          </div>

          <div className="remote-secret-row">
            <input
              type="text"
              className="input"
              value={manualUser}
              placeholder="直接添加飞书 open_id 到运行时白名单"
              disabled={busy}
              onChange={(event) => setManualUser(event.target.value)}
            />
            <button
              type="button"
              className="button"
              disabled={busy || !manualUser.trim()}
              onClick={() =>
                void runAction(async () => {
                  const next = await api.allowRemoteUser("feishu", manualUser.trim());
                  setManualUser("");
                  return next;
                }, "已添加到运行时白名单")
              }
            >
              添加用户
            </button>
          </div>
        </div>

        {status.pendingCodes.length > 0 ? (
          <div className="remote-sublist">
            <div className="field__label">待审批配对请求</div>
            {status.pendingCodes.map((item) => (
              <div key={item.code} className="remote-row">
                <div className="remote-row__info">
                  <span className="badge" data-tone="warn">{item.code}</span>
                  <span className="skill-card__path">{item.userId}</span>
                  <span className="remote-row__muted">会话 {item.chatId} · {formatStamp(item.created)}</span>
                </div>
                <button
                  type="button"
                  className="button button--primary"
                  disabled={busy}
                  onClick={() => void runAction(() => api.approvePairing(item.code), `已批准 ${item.userId}`)}
                >
                  批准
                </button>
              </div>
            ))}
          </div>
        ) : null}

        <div className="remote-sublist">
          <div className="field__label">已批准用户（运行时白名单）</div>
          {status.runtimeAllowlist.length === 0 ? (
            <div className="field__hint">暂无通过配对或手动加入的运行时用户</div>
          ) : (
            <div className="remote-chips">
              {status.runtimeAllowlist.map((entry) => (
                <span key={`${entry.adapter}:${entry.userId}`} className="badge remote-chip">
                  <span>{entry.adapter}: {entry.userId}</span>
                  <button
                    type="button"
                    className="remote-chip__remove"
                    disabled={busy}
                    onClick={() =>
                      void runAction(
                        () => api.revokeRemoteUser(entry.adapter, entry.userId),
                        `已移除 ${entry.userId}`,
                      )
                    }
                    aria-label={`移除 ${entry.userId}`}
                  >
                    ×
                  </button>
                </span>
              ))}
            </div>
          )}
        </div>
      </section>

      <section className="panel-card">
        <div className="panel-card__head">
          <div className="panel-card__title">
            <span>已绑定会话 ({status.bindings.length})</span>
          </div>
        </div>
        {status.bindings.length === 0 ? (
          <div className="panel-empty">还没有产生过远程会话绑定，在飞书发送第一条消息后会自动记录在这里。</div>
        ) : (
          <div className="remote-sublist">
            {status.bindings.map((binding) => (
              <div key={binding.key} className="remote-row">
                <div className="remote-row__info">
                  <strong>{binding.name || binding.key}</strong>
                  <span className="skill-card__path">{binding.cwd || status.defaultCwd}</span>
                  {binding.session ? (
                    <span className="remote-row__muted skill-card__path">{binding.session}</span>
                  ) : null}
                </div>
                <div className="skill-card__actions">
                  {binding.session ? (
                    <button
                      type="button"
                      className="button"
                      disabled={busy}
                      onClick={() => void onOpenSession(binding.session!)}
                    >
                      打开会话
                    </button>
                  ) : null}
                  <button
                    type="button"
                    className="button"
                    disabled={busy}
                    onClick={() => void runAction(() => api.unbindRemoteChat(binding.key), "已解除会话绑定")}
                  >
                    解绑
                  </button>
                </div>
              </div>
            ))}
          </div>
        )}
      </section>

      {status.recentLogs && status.recentLogs.length > 0 ? (
        <section className="panel-card">
          <div className="panel-card__head">
            <div className="panel-card__title">
              <span>网关日志（最近 {status.recentLogs.length} 条）</span>
            </div>
          </div>
          <pre className="remote-logs">{status.recentLogs.join("\n")}</pre>
        </section>
      ) : null}
    </div>
  );
}
