import { useEffect, useRef, useState } from "react";

// The input. Enter sends, Shift+Enter breaks the line, and the box grows with
// the draft up to a ceiling: a prompt that scrolls inside a 44px box is a
// prompt the reader cannot proofread.
export function Composer({
  disabled,
  onSend,
}: {
  disabled: boolean;
  onSend: (text: string) => void;
}) {
  const [value, setValue] = useState("");
  const area = useRef<HTMLTextAreaElement>(null);

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
            onKeyDown={(event) => {
              if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) {
                event.preventDefault();
                send();
              }
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
