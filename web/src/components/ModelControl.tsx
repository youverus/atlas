import { useEffect, useState } from "react";
import type { RefObject } from "react";
import type { ModelList } from "../api";
import { FooterPopover } from "./FooterPopover";

function formatWindow(value?: number): string {
  if (!value) return "";
  if (value >= 1000) return `${(value / 1000).toFixed(value >= 10000 ? 0 : 1)}k`;
  return String(value);
}

export function ModelButton({
  label,
  pending,
  open,
  onToggle,
  buttonRef,
}: {
  label: string;
  pending?: string;
  open: boolean;
  onToggle: () => void;
  buttonRef: RefObject<HTMLButtonElement | null>;
}) {
  return (
    <button
      ref={buttonRef}
      type="button"
      className="composer-meta__btn"
      data-active={open ? "true" : undefined}
      aria-expanded={open}
      aria-haspopup="dialog"
      onClick={onToggle}
      title={pending ? `下一条消息生效: ${pending}` : "切换模型"}
    >
      <span className="composer-meta__text">{label || "未配置模型"}</span>
    </button>
  );
}

export function ModelPopover({
  open,
  models,
  anchorRef,
  onSelect,
  onClose,
}: {
  open: boolean;
  models: ModelList | null;
  anchorRef: RefObject<HTMLButtonElement | null>;
  onSelect: (provider: string, id: string) => Promise<void>;
  onClose: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!open) return;
    setError("");
  }, [open]);

  const list = models?.models ?? [];

  const choose = async (provider: string, id: string) => {
    if (busy) return;
    const key = `${provider}/${id}`;
    if (key === models?.current) {
      onClose();
      return;
    }
    setBusy(true);
    setError("");
    try {
      await onSelect(provider, id);
      onClose();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  };

  return (
    <FooterPopover
      open={open}
      anchorRef={anchorRef}
      align="left"
      busy={busy}
      ariaLabel="切换模型"
      className="model-popover"
      onClose={onClose}
    >
      <div className="footer-popover__head">
        <span className="footer-popover__title">切换模型</span>
        <span className="permission-popover__hint">
          {models?.pending ? `下一条生效: ${models.pending}` : `${list.length} 个可用`}
        </span>
      </div>

      {error ? <div className="notice notice--error footer-popover__error">{error}</div> : null}

      {list.length > 0 ? (
        <div className="model-popover__list" role="radiogroup" aria-label="模型列表">
          {list.map((option) => {
            const key = `${option.provider}/${option.id}`;
            const selected = option.current || key === models?.current;
            const win = formatWindow(option.contextWindow);
            const metaParts = [key];
            if (win) metaParts.push(`${win} 上下文`);
            if (option.reasoning) metaParts.push("支持推理");
            return (
              <button
                key={key}
                type="button"
                role="radio"
                aria-checked={selected}
                className="model-option"
                data-active={selected ? "true" : undefined}
                disabled={busy}
                onClick={() => void choose(option.provider, option.id)}
              >
                <span className="model-option__top">
                  <span className="model-option__label">{option.name || option.id}</span>
                  {selected ? (
                    <span className="model-option__mark" aria-hidden="true">
                      ✓
                    </span>
                  ) : null}
                </span>
                <span className="model-option__meta">{metaParts.join(" · ")}</span>
              </button>
            );
          })}
        </div>
      ) : (
        <div className="field__hint">暂无可用模型，请从左侧「设置」中编辑模型配置。</div>
      )}
    </FooterPopover>
  );
}
