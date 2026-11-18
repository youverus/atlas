import { useEffect, useRef, useState } from "react";

// The input. Enter sends, Shift+Enter breaks the line, and the box grows with
// the draft up to a ceiling: a prompt that scrolls inside a 44px box is a
// prompt the reader cannot proofread.
//
// Enter is not always a send, though: while an input method is composing, the
// same key commits the candidate the reader is choosing. The window runs in a
// system webview, where that keypress reports isComposing false and arrives
// after compositionend, so the state is tracked here rather than read off the
// event — a candidate being chosen in Chinese must never post the half-written
// sentence.
export function Composer({
  disabled,
  onSend,
}: {
  disabled: boolean;
  onSend: (text: string) => void;
}) {
  const [value, setValue] = useState("");
  const area = useRef<HTMLTextAreaElement>(null);
  const composing = useRef(false);
  // True for exactly the keypress that ends a composition. WebKit fires
  // compositionend before the keydown of the committing Enter, so a flag that
  // was cleared with the composition would already be gone by then.
  const justComposed = useRef(false);

  useEffect(() => {
    const node = area.current;
    if (!node) return;
    node.style.height = "auto";
    node.style.height = `${Math.min(node.scrollHeight, 200)}px`;
  }, [value]);

  const send = () => {
    const text = value.trim();
    if (!text || disabled) return;
    onSend(text);
    setValue("");
  };

  return (
    <div className="composer">
      <div className="composer__inner">
        <div className="composer__box">
          <textarea
            ref={area}
            rows={1}
            value={value}
            placeholder="问点什么…"
            autoFocus
            onChange={(event) => setValue(event.target.value)}
            onCompositionStart={() => {
              composing.current = true;
            }}
            onCompositionEnd={() => {
              composing.current = false;
              justComposed.current = true;
              // The flag outlives this task and nothing else: the committing
              // keypress is dispatched in the same one.
              window.setTimeout(() => {
                justComposed.current = false;
              }, 0);
            }}
            onKeyDown={(event) => {
              if (event.key !== "Enter" || event.shiftKey) return;
              const native = event.nativeEvent;
              // 229 is what engines report when an IME owns the key.
              if (composing.current || justComposed.current || native.isComposing || native.keyCode === 229) {
                justComposed.current = false;
                return;
              }
              event.preventDefault();
              send();
            }}
          />
          <button
            type="button"
            className="send"
            onClick={send}
            disabled={disabled || value.trim().length === 0}
            aria-label="发送"
          >
            ↑
          </button>
        </div>
        <div className="composer__hint">
          <span className="kbd">Enter</span>
          <span>发送</span>
          <span className="kbd">Shift</span>
          <span className="kbd">Enter</span>
          <span>换行</span>
        </div>
      </div>
    </div>
  );
}
