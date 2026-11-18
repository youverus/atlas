import { useEffect, useRef, useState } from "react";
import type { ForkPoint } from "../api";

// Where a fork starts, and what it is called.
//
// Both questions are one sheet because they are one decision: a fork point with
// no name arrives in the rail beside the conversation it copied, and the reader
// is looking at two rows that claim to be the same thing. Asking them apart
// would also leave a fork that exists between the two answers.
//
// The list is the kernel's, not the page's. A fork point is a user message and
// nothing else: what a branch redecides is what was asked, and the answers
// follow from that. So the rows are the prompts in the order they were said,
// with the end of the conversation first — it is a point like any other, and
// putting it first means the common case needs no scrolling.
//
// The name field is uncontrolled on purpose. With `value` bound to state the
// input first mounts empty, and React writes the suggestion on a later commit —
// after the effect that focuses and selects has already run, which leaves the
// caret at the end instead of selecting the name. A fresh mount per open carries
// the suggestion in the DOM from the start, so select() sticks.
export function ForkDialog({
  open,
  points,
  suggested,
  busy,
  onCancel,
  onConfirm,
}: {
  open: boolean;
  points: ForkPoint[];
  suggested: string;
  busy: boolean;
  onCancel: () => void;
  onConfirm: (entryId: string, name: string) => void;
}) {
  const field = useRef<HTMLInputElement>(null);
  // Empty is the end of the conversation, which is the row the sheet opens on:
  // forking from the last thing said is the gesture the icon has always meant.
  const [entryId, setEntryId] = useState("");
  const chosen = points.find((point) => (point.entryId ?? "") === entryId);

  useEffect(() => {
    if (!open) return;
    setEntryId("");
    // Remembered before the focus below, so closing can hand the caret back:
    // a reader who cancels was very likely typing in the composer, and leaving
    // focus nowhere means their next keystroke goes nowhere.
    const previous = document.activeElement;
    const node = field.current;
    node?.focus();
    // Select rather than place the caret: the suggestion is a starting point,
    // and a reader who has their own name should be able to type over it.
    node?.select();
    return () => {
      if (previous instanceof HTMLElement && previous.isConnected) previous.focus();
    };
  }, [open]);

  if (!open) return null;

  return (
    <div
      className="sheet-backdrop"
      role="presentation"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget && !busy) onCancel();
      }}
    >
      <form
        className="sheet sheet--narrow"
        role="dialog"
        aria-modal="true"
        aria-label="分叉会话"
        onSubmit={(event) => {
          event.preventDefault();
          if (busy) return;
          onConfirm(entryId, field.current?.value.trim() ?? "");
        }}
        onKeyDown={(event) => {
          if (event.key === "Escape" && !busy) {
            event.preventDefault();
            onCancel();
          }
        }}
      >
        <div className="sheet__head">
          <span className="sheet__title">分叉会话</span>
          <button type="button" className="icon-button" onClick={onCancel} disabled={busy} aria-label="取消">
            ✕
          </button>
        </div>

        <div className="sheet__body">
          {points.length > 1 ? (
            <div className="field">
              <span className="field__label">从哪里分叉</span>
              <div className="fork-points" role="radiogroup" aria-label="从哪里分叉">
                {points.map((point) => {
                  const value = point.entryId ?? "";
                  return (
                    <button
                      key={value || "end"}
                      type="button"
                      role="radio"
                      aria-checked={value === entryId}
                      className="fork-point"
                      disabled={busy}
                      onClick={() => setEntryId(value)}
                    >
                      <span className="fork-point__index">{point.index}</span>
                      <span className="fork-point__text">{point.text || "整个对话"}</span>
                    </button>
                  );
                })}
              </div>
            </div>
          ) : null}

          <div className="field">
            <label className="field__label" htmlFor="fork-name">
              新会话的名字
            </label>
            <input
              id="fork-name"
              ref={field}
              className="field__input"
              defaultValue={suggested}
              maxLength={80}
              disabled={busy}
              placeholder="留空就沿用现在的名字"
            />
            <div className="field__hint">
              {chosen?.entryId
                ? `只保留第 ${chosen.index} 条之前的内容，原会话留在原处不动；被分掉的那句话会放回输入框，改完直接发。`
                : "分叉会复制这段对话，原会话留在原处不动，两条分支各自继续。"}
            </div>
          </div>

          <div className="sheet__actions">
            <button type="button" className="button" onClick={onCancel} disabled={busy}>
              取消
            </button>
            <button type="submit" className="button button--primary" disabled={busy}>
              {busy ? "分叉中…" : "分叉"}
            </button>
          </div>
        </div>
      </form>
    </div>
  );
}
