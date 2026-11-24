import { useCallback, useEffect, useRef, useState } from "react";
import type { PointerEvent as ReactPointerEvent, KeyboardEvent as ReactKeyboardEvent, RefObject } from "react";
import type { ModelList } from "../api";
import { FooterPopover } from "./FooterPopover";

/** Returns adjustable thinking effort levels for the active model, or [] when
 *  thinking effort is not configured for it. */
export function effortLevelsOf(models: ModelList | null): string[] {
  const levels = models?.levels ?? [];
  if (levels.length === 0) return [];
  if (levels.length === 1 && levels[0] === "off") return [];
  return levels;
}

/** Only `max` and above (`max`, `ultra`) trigger the peak effort effect. Models
 *  whose highest stop stops at `xhigh` do not trigger it. */
export function isPeakEffort(level: string): boolean {
  return level === "max" || level === "ultra";
}

function clamp(value: number, min: number, max: number): number {
  return Math.max(min, Math.min(max, value));
}

export function EffortButton({
  models,
  running,
  archived,
  open,
  onToggle,
  buttonRef,
}: {
  models: ModelList | null;
  running: boolean;
  archived: boolean;
  open: boolean;
  onToggle: () => void;
  buttonRef: RefObject<HTMLButtonElement | null>;
}) {
  const levels = effortLevelsOf(models);
  const configured = levels.length > 0;
  const disabled = !configured || running || archived;
  const current = models?.thinking || levels[0] || "";
  const label = configured ? `思考 · ${current}` : "思考 · 未配置";
  const peak = configured && !disabled && isPeakEffort(current);

  let title = "调整思考强度";
  if (!configured) {
    title = "当前模型未配置思考强度";
  } else if (archived) {
    title = "已归档会话为只读，无法调整思考强度";
  } else if (running) {
    title = "本轮运行中，结束后可调整思考强度";
  }

  let className = "chip chip--button chip--fixed";
  if (peak) {
    className += " chip--effort-peak";
  } else if (open && !disabled) {
    className += " chip--accent";
  }

  return (
    <button
      ref={buttonRef}
      type="button"
      className={className}
      disabled={disabled}
      aria-expanded={open && !disabled}
      aria-haspopup="dialog"
      data-level={configured ? current : undefined}
      onClick={onToggle}
      title={title}
    >
      {label}
    </button>
  );
}

export function EffortPopover({
  open,
  models,
  disabled,
  anchorRef,
  onSelect,
  onClose,
}: {
  open: boolean;
  models: ModelList | null;
  disabled: boolean;
  anchorRef: RefObject<HTMLButtonElement | null>;
  onSelect: (level: string) => Promise<void>;
  onClose: () => void;
}) {
  const levels = effortLevelsOf(models);
  const activeLevel = models?.thinking || levels[0] || "";
  const serverIndex = Math.max(0, levels.indexOf(activeLevel));
  const maxIndex = Math.max(0, levels.length - 1);

  const [draftIndex, setDraftIndex] = useState(serverIndex);
  const [ratio, setRatio] = useState(maxIndex > 0 ? serverIndex / maxIndex : 0);
  const [dragging, setDragging] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const trackRef = useRef<HTMLDivElement>(null);
  const pointerState = useRef<{
    id: number;
    startX: number;
    moved: boolean;
    snapped: number;
  } | null>(null);

  useEffect(() => {
    setDraftIndex(serverIndex);
    setRatio(maxIndex > 0 ? serverIndex / maxIndex : 0);
  }, [serverIndex, maxIndex, open]);

  useEffect(() => {
    if (!open) return;
    setError("");
  }, [open]);

  const commitLevel = useCallback(
    async (level: string, targetIndex: number) => {
      if (!level || level === activeLevel) return;
      setBusy(true);
      setError("");
      try {
        await onSelect(level);
      } catch (cause) {
        setError(cause instanceof Error ? cause.message : String(cause));
        setDraftIndex(serverIndex);
        setRatio(maxIndex > 0 ? serverIndex / maxIndex : 0);
      } finally {
        setBusy(false);
        if (targetIndex === serverIndex) {
          setRatio(maxIndex > 0 ? serverIndex / maxIndex : 0);
        }
      }
    },
    [activeLevel, maxIndex, onSelect, serverIndex],
  );

  const measurePointer = useCallback(
    (clientX: number) => {
      const track = trackRef.current;
      if (!track || maxIndex <= 0) {
        return { nextRatio: 0, snapped: 0 };
      }
      const rect = track.getBoundingClientRect();
      const nextRatio = rect.width > 0 ? clamp((clientX - rect.left) / rect.width, 0, 1) : 0;
      const snapped = clamp(Math.round(nextRatio * maxIndex), 0, maxIndex);
      return { nextRatio, snapped };
    },
    [maxIndex],
  );

  const snapToIndex = useCallback(
    (index: number, commit = true) => {
      const clamped = clamp(index, 0, maxIndex);
      setDraftIndex(clamped);
      setRatio(maxIndex > 0 ? clamped / maxIndex : 0);
      if (commit && levels[clamped]) {
        void commitLevel(levels[clamped], clamped);
      }
    },
    [commitLevel, levels, maxIndex],
  );

  const onPointerDown = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (busy || maxIndex <= 0 || event.button !== 0) return;
    event.preventDefault();
    event.currentTarget.setPointerCapture(event.pointerId);
    const { snapped } = measurePointer(event.clientX);
    pointerState.current = {
      id: event.pointerId,
      startX: event.clientX,
      moved: false,
      snapped,
    };
    setDraftIndex(snapped);
    // Do not set dragging yet so a quick click glides smoothly to the target stop.
    setRatio(maxIndex > 0 ? snapped / maxIndex : 0);
  };

  const onPointerMove = (event: ReactPointerEvent<HTMLDivElement>) => {
    const state = pointerState.current;
    if (!state || state.id !== event.pointerId || busy) return;
    if (!state.moved && Math.abs(event.clientX - state.startX) >= 3) {
      state.moved = true;
      setDragging(true);
    }
    if (!state.moved) return;
    const { nextRatio, snapped } = measurePointer(event.clientX);
    state.snapped = snapped;
    setRatio(nextRatio);
    setDraftIndex(snapped);
  };

  const finishPointer = (event: ReactPointerEvent<HTMLDivElement>, cancelled = false) => {
    const state = pointerState.current;
    if (!state || state.id !== event.pointerId) return;
    pointerState.current = null;
    if (event.currentTarget.hasPointerCapture(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId);
    }
    setDragging(false);
    if (cancelled) {
      snapToIndex(serverIndex, false);
      return;
    }
    snapToIndex(state.snapped, true);
  };

  const onKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    if (busy || maxIndex <= 0) return;
    let next: number | null = null;
    if (event.key === "ArrowRight" || event.key === "ArrowUp") next = draftIndex + 1;
    else if (event.key === "ArrowLeft" || event.key === "ArrowDown") next = draftIndex - 1;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = maxIndex;
    if (next === null) return;
    event.preventDefault();
    snapToIndex(next, true);
  };

  const active = Boolean(open && !disabled && levels.length > 0);
  const shownLevel = levels[draftIndex] ?? activeLevel;
  const peak = isPeakEffort(shownLevel);
  const percent = clamp(ratio * 100, 0, 100);

  return (
    <FooterPopover
      open={active}
      anchorRef={anchorRef}
      align="right"
      busy={busy}
      ariaLabel="思考强度"
      className="effort-popover"
      onClose={onClose}
    >
      <div className="footer-popover__head">
        <span className="footer-popover__title">思考强度</span>
        <span
          className="effort-popover__value"
          data-level={shownLevel}
          data-peak={peak ? "true" : undefined}
        >
          {shownLevel}
        </span>
      </div>

      {error ? <div className="notice notice--error footer-popover__error">{error}</div> : null}

      <div
        className="effort-slider"
        role="slider"
        tabIndex={busy ? -1 : 0}
        aria-label="思考强度滑块"
        aria-valuemin={0}
        aria-valuemax={maxIndex}
        aria-valuenow={draftIndex}
        aria-valuetext={shownLevel}
        aria-disabled={busy}
        data-level={shownLevel}
        data-peak={peak ? "true" : undefined}
        data-dragging={dragging ? "true" : undefined}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={(event) => finishPointer(event, false)}
        onPointerCancel={(event) => finishPointer(event, true)}
        onKeyDown={onKeyDown}
      >
        <div ref={trackRef} className="effort-slider__rail">
          <div className="effort-slider__fill" style={{ width: `${percent}%` }} />
          {levels.map((level, index) => {
            const stopPercent = maxIndex > 0 ? (index / maxIndex) * 100 : 0;
            const passed = stopPercent <= percent + 0.5;
            return (
              <span
                key={level}
                className="effort-slider__stop"
                data-passed={passed ? "true" : undefined}
                style={{ left: `${stopPercent}%` }}
              />
            );
          })}
          <div className="effort-slider__thumb" style={{ left: `${percent}%` }} />
        </div>
      </div>

      <div className="effort-ticks">
        {levels.map((level, index) => (
          <button
            key={level}
            type="button"
            className="effort-tick"
            data-level={level}
            data-peak={index === draftIndex && isPeakEffort(level) ? "true" : undefined}
            data-active={index === draftIndex ? "true" : undefined}
            disabled={busy}
            onClick={() => snapToIndex(index, true)}
          >
            {level}
          </button>
        ))}
      </div>
    </FooterPopover>
  );
}
