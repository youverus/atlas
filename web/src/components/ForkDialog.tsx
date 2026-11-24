import { useEffect, useRef, useState } from "react";

// Narrow modal dialog to name and confirm a fork before copying the branch.
export function ForkDialog({
  open,
  entryId,
  turnIndex,
  defaultName,
  submitting,
  error,
  onConfirm,
  onClose,
}: {
  open: boolean;
  entryId: string;
  turnIndex?: number;
  defaultName: string;
  submitting: boolean;
  error: string;
  onConfirm: (name: string) => void;
  onClose: () => void;
}) {
  const [name, setName] = useState(defaultName);
  const initialName = useRef(defaultName);
  initialName.current = defaultName;
  const inputRef = useRef<HTMLInputElement>(null);
  const composing = useRef(false);
  const justComposed = useRef(false);
  const suppressSubmit = useRef(false);

  useEffect(() => {
    if (!open) return;
    setName(initialName.current);
    const frame = window.requestAnimationFrame(() => {
      const node = inputRef.current;
      if (!node) return;
      node.focus();
      node.select();
    });
    return () => window.cancelAnimationFrame(frame);
  }, [open, entryId]);

  useEffect(() => {
    if (!open) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !submitting) {
        onClose();
      }
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [open, submitting, onClose]);

  if (!open) return null;

  const description = entryId
    ? `将保留到${turnIndex ? `第 ${turnIndex} 条回答` : "这条回答"}为止，复制出一条新分支；原会话保持不变。`
    : "将复制整段对话开一条新分支；原会话保持不变。";

  return (
    <div
      className="sheet-backdrop"
      role="presentation"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget && !submitting) onClose();
      }}
    >
      <div className="sheet sheet--narrow" role="dialog" aria-modal="true" aria-label="分叉会话">
        <div className="sheet__head">
          <span className="sheet__title">分叉会话</span>
          <button
            type="button"
            className="icon-button"
            onClick={onClose}
            disabled={submitting}
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
            if (submitting) return;
            onConfirm(name.trim());
          }}
        >
          {error ? <div className="notice notice--error">{error}</div> : null}

          <div className="field">
            <label className="field__label" htmlFor="fork-name-input">
              新会话名称
            </label>
            <input
              id="fork-name-input"
              ref={inputRef}
              className="field__input"
              value={name}
              placeholder={defaultName}
              disabled={submitting}
              onChange={(event) => setName(event.target.value)}
              onCompositionStart={() => {
                composing.current = true;
              }}
              onCompositionEnd={() => {
                composing.current = false;
                justComposed.current = true;
                window.setTimeout(() => {
                  justComposed.current = false;
                }, 0);
              }}
              onKeyDown={(event) => {
                if (event.key !== "Enter") return;
                const native = event.nativeEvent;
                if (composing.current || justComposed.current || native.isComposing || native.keyCode === 229) {
                  justComposed.current = false;
                  suppressSubmit.current = true;
                  window.setTimeout(() => {
                    suppressSubmit.current = false;
                  }, 0);
                }
              }}
            />
            <div className="field__hint">{description}</div>
            <div className="field__hint">已填入默认名称，可直接确认或改名；留空则沿用原会话名称。</div>
          </div>

          <div className="sheet__actions">
            <button type="button" className="button" onClick={onClose} disabled={submitting}>
              取消
            </button>
            <button type="submit" className="button button--primary" disabled={submitting}>
              {submitting ? "分叉中…" : "确认分叉"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
