import { useCallback, useEffect, useRef, useState } from "react";
import type { KeyboardEvent as ReactKeyboardEvent, PointerEvent as ReactPointerEvent } from "react";

const CHAT_FLOOR = 200;
const KEY_STEP = 20;

export type VerticalResizeHandleProps = {
  role: "separator";
  tabIndex: number;
  "aria-orientation": "horizontal";
  "aria-label": string;
  "aria-valuenow": number;
  "aria-valuemin": number;
  "aria-valuemax": number;
  onPointerDown: (event: ReactPointerEvent<HTMLElement>) => void;
  onKeyDown: (event: ReactKeyboardEvent<HTMLElement>) => void;
  onDoubleClick: () => void;
};

/**
 * Persisted vertical panel height hook modeled after useColumnWidth, dragging
 * the top edge upward to grow the bottom panel while reserving room for chat.
 */
export function usePanelHeight({
  storageKey,
  label,
  initial,
  min,
  max,
}: {
  storageKey: string;
  label: string;
  initial: number;
  min: number;
  max: number;
}): { height: number; dragging: boolean; handleProps: VerticalResizeHandleProps } {
  const [height, setHeight] = useState(() => clampHeight(readHeight(storageKey, initial), min, max));
  const [dragging, setDragging] = useState(false);
  const origin = useRef({ y: 0, height });
  const latest = useRef(height);
  const live = useRef(false);

  const apply = useCallback(
    (next: number) => {
      const value = clampHeight(next, min, max);
      latest.current = value;
      setHeight(value);
    },
    [min, max],
  );

  const commit = useCallback(
    (next: number) => {
      apply(next);
      try {
        window.localStorage.setItem(storageKey, String(latest.current));
      } catch {
        // Ignore storage errors in private mode.
      }
    },
    [apply, storageKey],
  );

  useEffect(() => {
    if (!dragging) return;
    const body = document.body.style;
    const { cursor, userSelect } = body;
    body.cursor = "row-resize";
    body.userSelect = "none";
    return () => {
      body.cursor = cursor;
      body.userSelect = userSelect;
    };
  }, [dragging]);

  useEffect(() => {
    if (!dragging) return;

    const settle = () => {
      if (!live.current) return;
      live.current = false;
      setDragging(false);
      commit(latest.current);
    };

    const move = (event: PointerEvent) => {
      if (!live.current) return;
      if (event.buttons === 0) {
        settle();
        return;
      }
      const deltaY = origin.current.y - event.clientY;
      apply(origin.current.height + deltaY);
    };

    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", settle);
    window.addEventListener("pointercancel", settle);
    return () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", settle);
      window.removeEventListener("pointercancel", settle);
    };
  }, [dragging, apply, commit]);

  useEffect(() => {
    const onResize = () => apply(latest.current);
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, [apply]);

  const handleProps: VerticalResizeHandleProps = {
    role: "separator",
    tabIndex: 0,
    "aria-orientation": "horizontal",
    "aria-label": label,
    "aria-valuenow": height,
    "aria-valuemin": min,
    "aria-valuemax": max,
    onPointerDown: (event) => {
      if (event.button !== 0) return;
      event.preventDefault();
      origin.current = { y: event.clientY, height: latest.current };
      live.current = true;
      setDragging(true);
    },
    onKeyDown: (event) => {
      if (event.key === "ArrowUp") {
        event.preventDefault();
        commit(latest.current + KEY_STEP);
      } else if (event.key === "ArrowDown") {
        event.preventDefault();
        commit(latest.current - KEY_STEP);
      }
    },
    onDoubleClick: () => {
      commit(initial);
    },
  };

  return { height, dragging, handleProps };
}

function readHeight(key: string, fallback: number): number {
  try {
    const raw = window.localStorage.getItem(key);
    if (!raw) return fallback;
    const parsed = Number(raw);
    return Number.isFinite(parsed) ? parsed : fallback;
  } catch {
    return fallback;
  }
}

function clampHeight(value: number, min: number, max: number): number {
  const viewportMax =
    typeof window !== "undefined" ? Math.max(min, window.innerHeight - CHAT_FLOOR) : max;
  const ceiling = Math.min(max, viewportMax);
  return Math.round(Math.max(min, Math.min(ceiling, value)));
}
