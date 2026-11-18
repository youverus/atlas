import { useEffect, useState } from "react";
import { api, type ModelList, type PermissionState, type Stats } from "../api";
import type { Theme } from "../lib/useTheme";

// The settings sheet: model, reasoning effort, permission posture, appearance,
// and the usage readout. It is a sheet rather than a route because it is a
// detour from the conversation, not a place the reader lives.
export function SettingsSheet({
  open,
  theme,
  onTheme,
  onClose,
  onModelChanged,
  onPermissionChanged,
}: {
  open: boolean;
  theme: Theme;
  onTheme: (theme: Theme) => void;
  onClose: () => void;
  onModelChanged: () => void;
  onPermissionChanged: () => void;
}) {
  const [models, setModels] = useState<ModelList | null>(null);
  const [stats, setStats] = useState<Stats | null>(null);
  const [permission, setPermission] = useState<PermissionState | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!open) return;
    let live = true;
    setError("");
    Promise.all([api.models(), api.stats(), api.permission()])
      .then(([catalog, usage, gate]) => {
        if (!live) return;
        setModels(catalog);
        setStats(usage);
        setPermission(gate);
      })
      .catch((cause: unknown) => {
        if (!live) return;
        setError(cause instanceof Error ? cause.message : String(cause));
      });
    return () => {
      live = false;
    };
  }, [open]);

  if (!open) return null;

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
      // The answer is the state that took effect, so the rows and the selection
      // come back from the same call that changed them.
      setPermission(await api.setPermission(value));
      onPermissionChanged();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  };

  const current = models?.models.find((item) => item.current);
  const percent =
    stats && stats.contextWindow > 0
      ? Math.min(100, Math.round((stats.contextTokens / stats.contextWindow) * 100))
      : 0;

  return (
    <div
      className="sheet-backdrop"
      role="presentation"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div className="sheet" role="dialog" aria-modal="true" aria-label="设置">
        <div className="sheet__head">
          <span className="sheet__title">设置</span>
          <button type="button" className="icon-button" onClick={onClose} aria-label="关闭">
            ✕
          </button>
        </div>

        <div className="sheet__body">
          {error ? <div className="notice notice--error">{error}</div> : null}

          <div className="field">
            <div className="field__label">模型</div>
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
                    {option.provider} / {option.name || option.id}
                  </option>
                );
              })}
            </select>
            {current?.contextWindow ? (
              <div className="field__hint">
                上下文窗口 {format(current.contextWindow)} tokens
                {current.reasoning ? " · 支持推理" : ""}
              </div>
            ) : null}
          </div>

          {models?.levels && models.levels.length > 0 ? (
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

          {stats ? (
            <div className="field">
              <div className="field__label">用量</div>
              <div className="stat-grid">
                <div className="stat">
                  <div className="stat__label">消息</div>
                  <div className="stat__value">{stats.totalMessages}</div>
                </div>
                <div className="stat">
                  <div className="stat__label">工具调用</div>
                  <div className="stat__value">{stats.toolCalls}</div>
                </div>
                <div className="stat">
                  <div className="stat__label">输入 tokens</div>
                  <div className="stat__value">{format(stats.inputTokens)}</div>
                </div>
                <div className="stat">
                  <div className="stat__label">输出 tokens</div>
                  <div className="stat__value">{format(stats.outputTokens)}</div>
                </div>
                <div className="stat" style={{ gridColumn: "1 / -1" }}>
                  <div className="stat__label">
                    上下文 {format(stats.contextTokens)} / {format(stats.contextWindow)} · {percent}%
                  </div>
                  <div className="meter">
                    <div className="meter__fill" style={{ width: `${percent}%` }} />
                  </div>
                </div>
                {stats.cost > 0 ? (
                  <div className="stat" style={{ gridColumn: "1 / -1" }}>
                    <div className="stat__label">花费</div>
                    <div className="stat__value">${stats.cost.toFixed(4)}</div>
                  </div>
                ) : null}
              </div>
              {stats.sessionFile ? (
                <div className="field__hint" style={{ wordBreak: "break-all" }}>
                  {stats.sessionFile}
                </div>
              ) : null}
            </div>
          ) : null}
        </div>
      </div>
    </div>
  );
}

function format(value: number): string {
  if (!value) return "0";
  if (value >= 1000) return `${(value / 1000).toFixed(value >= 10000 ? 0 : 1)}k`;
  return String(value);
}
