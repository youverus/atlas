import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import type { ReactNode, RefObject } from "react";

/** Shared floating panel anchored above a button in the composer footer row,
 *  handling horizontal clamping and Escape / outside-click dismissal. */
export function FooterPopover({
  open,
  anchorRef,
  align = "left",
  busy = false,
  ariaLabel,
  className,
  onClose,
  children,
}: {
  open: boolean;
  anchorRef: RefObject<HTMLButtonElement | null>;
  align?: "left" | "right";
  busy?: boolean;
  ariaLabel: string;
  className?: string;
  onClose: () => void;
  children: ReactNode;
}) {
  const [leftOffset, setLeftOffset] = useState(0);
  const popoverRef = useRef<HTMLDivElement>(null);

  const reposition = useCallback(() => {
    const anchor = anchorRef.current;
    const popover = popoverRef.current;
    const parent = popover?.offsetParent as HTMLElement | null;
    if (!anchor || !popover || !parent) return;
    const anchorRect = anchor.getBoundingClientRect();
    const parentRect = parent.getBoundingClientRect();
    const maxLeft = Math.max(0, parentRect.width - popover.offsetWidth);
    const rawLeft =
      align === "right"
        ? anchorRect.right - parentRect.left - popover.offsetWidth
        : anchorRect.left - parentRect.left;
    setLeftOffset(Math.max(0, Math.min(rawLeft, maxLeft)));
  }, [align, anchorRef]);

  useLayoutEffect(() => {
    if (!open) return;
    reposition();
    window.addEventListener("resize", reposition);
    return () => window.removeEventListener("resize", reposition);
  }, [open, reposition]);

  useEffect(() => {
    if (!open) return;
    const onMouseDown = (event: MouseEvent) => {
      const target = event.target as Node | null;
      if (!target) return;
      if (popoverRef.current?.contains(target) || anchorRef.current?.contains(target)) {
        return;
      }
      onClose();
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !busy) {
        onClose();
      }
    };
    document.addEventListener("mousedown", onMouseDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("mousedown", onMouseDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [open, busy, anchorRef, onClose]);

  if (!open) return null;

  return (
    <div
      ref={popoverRef}
      className={className ? `footer-popover ${className}` : "footer-popover"}
      role="dialog"
      aria-label={ariaLabel}
      style={{ left: `${leftOffset}px` }}
    >
      {children}
    </div>
  );
}
