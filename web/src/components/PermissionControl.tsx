import { useEffect, useState } from "react";
import type { RefObject } from "react";
import type { PermissionState } from "../api";
import { FooterPopover } from "./FooterPopover";

function isLoudMode(mode: string): boolean {
  return mode === "bypassPermissions" || mode === "off";
}

function permissionLabel(state: PermissionState): string {
  return state.modes.find((mode) => mode.value === state.mode)?.label ?? state.mode;
}

export function PermissionButton({
  permission,
  open,
  onToggle,
  buttonRef,
}: {
  permission: PermissionState | null;
  open: boolean;
  onToggle: () => void;
  buttonRef: RefObject<HTMLButtonElement | null>;
}) {
  if (!permission) return null;

  const loud = isLoudMode(permission.mode);
  const label = permissionLabel(permission);

  return (
    <button
      ref={buttonRef}
      type="button"
      className="composer-meta__btn composer-meta__btn--fixed"
      data-warn={loud ? "true" : undefined}
      data-active={open ? "true" : undefined}
      aria-expanded={open}
      aria-haspopup="dialog"
      onClick={onToggle}
      title="切换权限模式"
    >
      <span className="composer-meta__text">{label}</span>
    </button>
  );
}

export function PermissionPopover({
  open,
  permission,
  anchorRef,
  onSelect,
  onClose,
}: {
  open: boolean;
  permission: PermissionState | null;
  anchorRef: RefObject<HTMLButtonElement | null>;
  onSelect: (mode: string) => Promise<void>;
  onClose: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!open) return;
    setError("");
  }, [open]);

  const modes = permission?.modes ?? [];
  const active = Boolean(open && permission && modes.length > 0);

  const choose = async (mode: string) => {
    if (!permission || busy) return;
    if (mode === permission.mode) {
      onClose();
      return;
    }
    setBusy(true);
    setError("");
    try {
      await onSelect(mode);
      onClose();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  };

  return (
    <FooterPopover
      open={active}
      anchorRef={anchorRef}
      align="left"
      busy={busy}
      ariaLabel="权限模式"
      className="permission-popover"
      onClose={onClose}
    >
      <div className="footer-popover__head">
        <span className="footer-popover__title">权限模式</span>
        <span className="permission-popover__hint">仅本次运行</span>
      </div>

      {error ? <div className="notice notice--error footer-popover__error">{error}</div> : null}

      <div className="permission-list" role="radiogroup" aria-label="权限模式">
        {modes.map((option) => {
          const selected = option.value === permission?.mode;
          return (
            <button
              key={option.value}
              type="button"
              role="radio"
              aria-checked={selected}
              className="permission-option"
              data-active={selected ? "true" : undefined}
              data-warn={isLoudMode(option.value) ? "true" : undefined}
              disabled={busy}
              onClick={() => void choose(option.value)}
            >
              <span className="permission-option__top">
                <span className="permission-option__label">{option.label}</span>
                {selected ? <span className="permission-option__mark" aria-hidden="true">✓</span> : null}
              </span>
              {option.note ? <span className="permission-option__note">{option.note}</span> : null}
            </button>
          );
        })}
      </div>
    </FooterPopover>
  );
}
