import { useEffect, useId, useState } from "react";
import {
  api,
  ApiError,
  type ModelList,
  type ScheduleInput,
  type ScheduleList,
  type ScheduledTask,
} from "../api";
import { MarkdownBody } from "./MarkdownBody";

interface TaskDraft {
  id: string | null; // null when creating a new task
  name: string;
  enabled: boolean;
  prompt: string;
  model: string;
  cwd: string;
  sessionMode: "new" | "continue";
  scheduleType: "daily" | "interval";
  timeValue: string; // HH:MM
  weekdays: number[];
  intervalValue: number;
  intervalUnit: "minutes" | "hours";
}

const WEEKDAY_LABELS = ["日", "一", "二", "三", "四", "五", "六"];

function newDraft(): TaskDraft {
  return {
    id: null,
    name: "",
    enabled: true,
    prompt: "",
    model: "",
    cwd: "",
    sessionMode: "new",
    scheduleType: "daily",
    timeValue: "09:00",
    weekdays: [],
    intervalValue: 30,
    intervalUnit: "minutes",
  };
}

function draftFromTask(task: ScheduledTask): TaskDraft {
  const mins = Math.max(1, task.intervalMinutes || 30);
  const useHours = mins >= 60 && mins % 60 === 0;
  return {
    id: task.id,
    name: task.name,
    enabled: task.enabled,
    prompt: task.prompt,
    model: task.model ?? "",
    cwd: task.cwd ?? "",
    sessionMode: task.sessionMode === "continue" ? "continue" : "new",
    scheduleType: task.scheduleType === "interval" ? "interval" : "daily",
    timeValue: `${String(task.hour).padStart(2, "0")}:${String(task.minute).padStart(2, "0")}`,
    weekdays: [...(task.weekdays ?? [])],
    intervalValue: useHours ? mins / 60 : mins,
    intervalUnit: useHours ? "hours" : "minutes",
  };
}

function inputFromDraft(draft: TaskDraft): ScheduleInput {
  const [hRaw, mRaw] = draft.timeValue.split(":");
  const hour = Math.min(23, Math.max(0, Number.parseInt(hRaw || "9", 10) || 0));
  const minute = Math.min(59, Math.max(0, Number.parseInt(mRaw || "0", 10) || 0));
  const intervalBase = Math.max(1, Number(draft.intervalValue) || 1);
  const intervalMinutes = draft.intervalUnit === "hours" ? intervalBase * 60 : intervalBase;

  return {
    name: draft.name.trim(),
    enabled: draft.enabled,
    prompt: draft.prompt.trim(),
    model: draft.model.trim(),
    cwd: draft.cwd.trim(),
    sessionMode: draft.sessionMode,
    scheduleType: draft.scheduleType,
    hour,
    minute,
    weekdays: draft.scheduleType === "daily" ? [...draft.weekdays].sort((a, b) => a - b) : [],
    intervalMinutes: draft.scheduleType === "interval" ? intervalMinutes : 0,
  };
}

function formatRule(task: ScheduledTask): string {
  if (task.scheduleType === "interval") {
    const mins = Math.max(1, task.intervalMinutes || 1);
    if (mins >= 60 && mins % 60 === 0) {
      return `每隔 ${mins / 60} 小时`;
    }
    return `每隔 ${mins} 分钟`;
  }
  const hhmm = `${String(task.hour).padStart(2, "0")}:${String(task.minute).padStart(2, "0")}`;
  if (!task.weekdays || task.weekdays.length === 0) {
    return `每天 ${hhmm}`;
  }
  return `每周${task.weekdays.map((d) => WEEKDAY_LABELS[d] ?? d).join("、")} ${hhmm}`;
}

function formatStamp(iso?: string): string {
  if (!iso) return "";
  const dt = new Date(iso);
  if (Number.isNaN(dt.getTime())) return iso;
  return dt.toLocaleString("zh-CN", { hour12: false });
}

function formatDuration(ms?: number): string {
  if (!ms || ms <= 0) return "";
  if (ms < 1000) return `${ms}ms`;
  return `${(ms / 1000).toFixed(1)}s`;
}

export function SchedulePanel({
  data,
  models,
  onDataChange,
  onError,
  onOpenSession,
}: {
  data: ScheduleList | null;
  models: ModelList | null;
  onDataChange: (next: ScheduleList) => void;
  onError: (message: string) => void;
  onOpenSession: (path: string) => Promise<void> | void;
}) {
  const [draft, setDraft] = useState<TaskDraft | null>(null);
  const [busy, setBusy] = useState(false);

  const anyRunning = Boolean(data?.tasks.some((t) => t.running));

  useEffect(() => {
    if (!anyRunning) return;
    const timer = window.setInterval(() => {
      void api.schedules().then(onDataChange).catch(() => {});
    }, 2000);
    return () => window.clearInterval(timer);
  }, [anyRunning, onDataChange]);

  const reload = async () => {
    onDataChange(await api.schedules());
  };

  const pickCwd = async () => {
    try {
      const chosen = await api.pickFolder();
      if (chosen.path) {
        setDraft((prev) => (prev ? { ...prev, cwd: chosen.path! } : prev));
      }
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 501) {
        onError("当前浏览器模式不支持系统文件夹选择器，请直接输入目录路径。");
        return;
      }
      onError(cause instanceof Error ? cause.message : String(cause));
    }
  };

  const saveDraft = async () => {
    if (!draft) return;
    setBusy(true);
    onError("");
    try {
      const payload = inputFromDraft(draft);
      if (draft.id) {
        await api.updateSchedule(draft.id, payload);
      } else {
        await api.createSchedule(payload);
      }
      await reload();
      setDraft(null);
    } catch (cause) {
      onError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  };

  const toggleEnabled = async (task: ScheduledTask) => {
    setBusy(true);
    onError("");
    try {
      await api.updateSchedule(task.id, {
        name: task.name,
        enabled: !task.enabled,
        prompt: task.prompt,
        model: task.model ?? "",
        cwd: task.cwd ?? "",
        sessionMode: task.sessionMode,
        scheduleType: task.scheduleType,
        hour: task.hour,
        minute: task.minute,
        weekdays: task.weekdays ?? [],
        intervalMinutes: task.intervalMinutes ?? 0,
      });
      await reload();
    } catch (cause) {
      onError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  };

  const runNow = async (task: ScheduledTask) => {
    setBusy(true);
    onError("");
    try {
      await api.runScheduleNow(task.id);
      await reload();
    } catch (cause) {
      onError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  };

  const removeTask = async (task: ScheduledTask) => {
    setBusy(true);
    onError("");
    try {
      await api.deleteSchedule(task.id);
      await reload();
    } catch (cause) {
      onError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  };

  const toggleWeekday = (day: number) => {
    setDraft((prev) => {
      if (!prev) return prev;
      const exists = prev.weekdays.includes(day);
      return {
        ...prev,
        weekdays: exists ? prev.weekdays.filter((d) => d !== day) : [...prev.weekdays, day],
      };
    });
  };

  if (!data) {
    return <div className="panel-empty">正在读取定时任务…</div>;
  }

  const tasks = data.tasks ?? [];
  const modelOptions = models?.models ?? [];
  const hasCustomModel =
    Boolean(draft?.model) &&
    !modelOptions.some((item) => `${item.provider}/${item.id}` === draft?.model);

  if (draft) {
    return (
      <section className="schedule-form remote-form">
        <div className="schedule-toolbar schedule-form__head">
          <h2>{draft.id ? "编辑定时任务" : "新建定时任务"}</h2>
          <label className="remote-check">
            <input
              type="checkbox"
              checked={draft.enabled}
              disabled={busy}
              onChange={(event) => setDraft({ ...draft, enabled: event.target.checked })}
            />
            <span>启用定时触发</span>
          </label>
        </div>

        <div className="remote-grid">
          <label className="field">
            <span className="field__label">任务名称</span>
            <input
              type="text"
              className="input"
              value={draft.name}
              autoFocus
              placeholder="例如：每日代码与日志巡检"
              disabled={busy}
              onChange={(event) => setDraft({ ...draft, name: event.target.value })}
            />
          </label>

          <label className="field">
            <span className="field__label">执行模型</span>
            <select
              className="select"
              value={draft.model}
              disabled={busy}
              onChange={(event) => setDraft({ ...draft, model: event.target.value })}
            >
              <option value="">
                跟随系统默认模型{models?.current ? ` (${models.current})` : ""}
              </option>
              {hasCustomModel ? (
                <option value={draft.model}>{draft.model}（当前配置）</option>
              ) : null}
              {modelOptions.map((item) => {
                const full = `${item.provider}/${item.id}`;
                return (
                  <option key={full} value={full}>
                    {item.name && item.name !== item.id ? `${full} — ${item.name}` : full}
                  </option>
                );
              })}
            </select>
          </label>
        </div>

        <div className="field">
          <span className="field__label">启动目录</span>
          <div className="remote-secret-row">
            <input
              type="text"
              className="input"
              value={draft.cwd}
              placeholder={data.defaultCwd ? `留空使用默认目录 (${data.defaultCwd})` : "留空使用系统启动目录"}
              disabled={busy}
              onChange={(event) => setDraft({ ...draft, cwd: event.target.value })}
            />
            <button type="button" className="button" disabled={busy} onClick={() => void pickCwd()}>
              选择目录
            </button>
          </div>
        </div>

        <div className="remote-grid">
          <div className="field">
            <span className="field__label">触发方式</span>
            <div className="segmented">
              <button
                type="button"
                aria-pressed={draft.scheduleType === "daily"}
                disabled={busy}
                onClick={() => setDraft({ ...draft, scheduleType: "daily" })}
              >
                每天固定时间
              </button>
              <button
                type="button"
                aria-pressed={draft.scheduleType === "interval"}
                disabled={busy}
                onClick={() => setDraft({ ...draft, scheduleType: "interval" })}
              >
                固定时间间隔
              </button>
            </div>
          </div>

          <div className="field">
            <span className="field__label">会话策略</span>
            <div className="segmented">
              <button
                type="button"
                aria-pressed={draft.sessionMode === "new"}
                disabled={busy}
                onClick={() => setDraft({ ...draft, sessionMode: "new" })}
              >
                每次新开会话
              </button>
              <button
                type="button"
                aria-pressed={draft.sessionMode === "continue"}
                disabled={busy}
                onClick={() => setDraft({ ...draft, sessionMode: "continue" })}
              >
                延续上次会话
              </button>
            </div>
          </div>
        </div>

        {draft.scheduleType === "daily" ? (
          <div className="remote-grid">
            <label className="field">
              <span className="field__label">执行时间</span>
              <input
                type="time"
                className="input"
                value={draft.timeValue}
                disabled={busy}
                onChange={(event) => setDraft({ ...draft, timeValue: event.target.value })}
              />
            </label>
            <div className="field">
              <span className="field__label">星期过滤（不选代表每天执行）</span>
              <div className="model-editor__gear-pills">
                {WEEKDAY_LABELS.map((label, day) => (
                  <button
                    key={day}
                    type="button"
                    className="model-editor__gear-pill"
                    aria-pressed={draft.weekdays.includes(day)}
                    disabled={busy}
                    onClick={() => toggleWeekday(day)}
                  >
                    周{label}
                  </button>
                ))}
              </div>
            </div>
          </div>
        ) : (
          <div className="field">
            <span className="field__label">执行周期（每隔多久触发一次）</span>
            <div className="remote-secret-row">
              <input
                type="number"
                min={1}
                className="input schedule-interval-input"
                value={draft.intervalValue}
                disabled={busy}
                onChange={(event) =>
                  setDraft({ ...draft, intervalValue: Math.max(1, Number(event.target.value) || 1) })
                }
              />
              <select
                className="select schedule-unit-select"
                value={draft.intervalUnit}
                disabled={busy}
                onChange={(event) =>
                  setDraft({
                    ...draft,
                    intervalUnit: event.target.value === "hours" ? "hours" : "minutes",
                  })
                }
              >
                <option value="minutes">分钟</option>
                <option value="hours">小时</option>
              </select>
            </div>
          </div>
        )}

        <label className="field">
          <span className="field__label">任务提示词 (Prompt)</span>
          <textarea
            className="input schedule-prompt-input"
            rows={4}
            value={draft.prompt}
            placeholder="输入每次触发时发送给 Agent 的指令…"
            disabled={busy}
            onChange={(event) => setDraft({ ...draft, prompt: event.target.value })}
          />
        </label>

        <div className="remote-form__actions">
          <button type="button" className="button" disabled={busy} onClick={() => setDraft(null)}>
            取消
          </button>
          <button
            type="button"
            className="button button--primary"
            disabled={busy}
            onClick={() => void saveDraft()}
          >
            保存任务
          </button>
        </div>
      </section>
    );
  }

  return (
    <div className="control-panel__sections">
      <div className="schedule-toolbar">
        <span className="schedule-count">{tasks.length} 个任务</span>
        <div className="schedule-actions">
          <button
            type="button"
            className="button"
            disabled={busy}
            onClick={() => void reload().catch((e) => onError(e instanceof Error ? e.message : String(e)))}
          >
            刷新
          </button>
          <button
            type="button"
            className="button button--primary"
            disabled={busy}
            onClick={() => {
              onError("");
              setDraft(newDraft());
            }}
          >
            <span aria-hidden="true">+</span> 新建定时任务
          </button>
        </div>
      </div>

      {tasks.length === 0 ? (
        <div className="schedule-empty">暂无定时任务</div>
      ) : (
        <div className="schedule-grid">
          {tasks.map((task) => (
            <ScheduleCard
              key={task.id}
              task={task}
              defaultCwd={data.defaultCwd}
              busy={busy}
              onRun={() => void runNow(task)}
              onEdit={() => {
                onError("");
                setDraft(draftFromTask(task));
              }}
              onToggle={() => void toggleEnabled(task)}
              onDelete={() => void removeTask(task)}
              onOpenSession={onOpenSession}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function ScheduleCard({
  task,
  defaultCwd,
  busy,
  onRun,
  onEdit,
  onToggle,
  onDelete,
  onOpenSession,
}: {
  task: ScheduledTask;
  defaultCwd?: string;
  busy: boolean;
  onRun: () => void;
  onEdit: () => void;
  onToggle: () => void;
  onDelete: () => void;
  onOpenSession: (path: string) => Promise<void> | void;
}) {
  const detailsId = useId();
  const [expanded, setExpanded] = useState(false);
  const disabled = busy || task.running;
  const lastStatus = task.lastStatus === "error" ? "上次失败" : task.lastStatus === "success" ? "上次成功" : "";
  const status = task.running
    ? { label: "运行中", tone: "warn" }
    : !task.enabled
      ? { label: "已暂停", tone: "muted" }
      : task.lastStatus === "error"
        ? { label: "上次失败", tone: "danger" }
        : { label: lastStatus || "等待执行", tone: "ok" };
  const lastSession = task.lastSession;
  const duration = formatDuration(task.lastDurationMs);

  return (
    <article className="schedule-card">
      <header className="schedule-card__head">
        <span className="schedule-card__icon" aria-hidden="true">◷</span>
        <h2 className="schedule-card__title" title={task.name}>{task.name}</h2>
        <span className="schedule-status" data-tone={status.tone}>{status.label}</span>
      </header>
      <p className="schedule-card__prompt" data-expanded={expanded}>{task.prompt}</p>
      <div className="schedule-card__timing">
        <span className="schedule-rule" data-enabled={task.enabled}>{formatRule(task)}</span>
        {task.enabled && task.nextRunAt ? <span>下次执行 {formatStamp(task.nextRunAt)}</span> : null}
      </div>
      <div className="schedule-card__bar">
        <button
          type="button"
          className="schedule-card__details-toggle"
          aria-expanded={expanded}
          aria-controls={detailsId}
          onClick={() => setExpanded(!expanded)}
        >
          详情
        </button>
        <div className="schedule-actions">
          <button type="button" className="button" disabled={disabled} onClick={onEdit}>编辑</button>
          <button type="button" className="button" disabled={disabled} onClick={onRun}>立即执行</button>
        </div>
      </div>
      <div id={detailsId} className="schedule-card__details" hidden={!expanded}>
        {expanded ? (
          <>
            <dl className="schedule-card__facts">
              <dt>执行模型</dt>
              <dd>{task.model || "跟随默认模型"}</dd>
              <dt>启动目录</dt>
              <dd>{task.cwd || defaultCwd || "工作区根目录"}</dd>
              <dt>会话策略</dt>
              <dd>{task.sessionMode === "continue" ? "延续上次会话" : "每次新开会话"}</dd>
              {task.lastRunAt ? (
                <>
                  <dt>上次执行</dt>
                  <dd>
                    {[formatStamp(task.lastRunAt), lastStatus, duration, task.runCount ? `累计 ${task.runCount} 次` : ""]
                      .filter(Boolean)
                      .join(" · ")}
                  </dd>
                </>
              ) : null}
            </dl>
            {task.lastSummary ? (
              <div className="schedule-summary"><MarkdownBody text={task.lastSummary} /></div>
            ) : null}
            {task.lastError ? <div className="notice notice--error schedule-card__error">{task.lastError}</div> : null}
            <div className="schedule-actions">
              {lastSession ? (
                <button type="button" className="button" disabled={busy} onClick={() => void onOpenSession(lastSession)}>
                  打开上次会话
                </button>
              ) : null}
              <button type="button" className="button" disabled={disabled} onClick={onToggle}>
                {task.enabled ? "暂停" : "启用"}
              </button>
              <button type="button" className="model-editor__danger-btn" disabled={disabled} onClick={onDelete}>
                删除
              </button>
            </div>
          </>
        ) : null}
      </div>
    </article>
  );
}
