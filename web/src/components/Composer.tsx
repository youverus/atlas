import { useEffect, useRef, useState } from "react";
import { leadingCommand, matches } from "../lib/commands";
import { CommandPalette } from "./CommandPalette";

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
//
// A leading slash opens the command palette above the box. It lists only what
// the window can run, and picking a row completes the line rather than firing
// it: the reader still presses Enter, which is the same gesture that runs a
// command in the terminal.
//
// The command itself is painted, not just accepted: a line that will be
// interpreted as a command looks different from a line that will be sent to the
// model, and the reader can see that before pressing Enter. The paint is a
// second, transparent-text layer behind the textarea rather than styled text
// inside it, because a textarea cannot hold a span. Only the name is marked —
// the argument stays plain — and only an exact name counts, so a half-typed
// prefix is not painted as something it is not yet.
export function Composer({
  disabled,
  onSend,
  seed,
  plain,
  placeholder,
}: {
  disabled: boolean;
  onSend: (text: string) => void;
  // Text to put in the box, replacing the draft. A new object every time, so
  // being handed the same sentence twice still lands: the identity is the
  // signal, not the text.
  seed?: { text: string } | null;
  /** 侧面板里的那个输入框。它只把句子发给侧对话，没有命令表可以补全，所以两
   *  件事一起关掉：不画补全面板，也不把它认出来的名字标成命令。列一张点不动
   *  的表比不列更糟——会让读者以为那些命令在这里能跑。 */
  plain?: boolean;
  /** 占位文字。两个输入框要能一眼分清「这句话发给谁」，而位置在窄窗口里不够。 */
  placeholder?: string;
}) {
  const [value, setValue] = useState("");
  // Esc closes the palette without touching the draft. Typing reopens it, which
  // is why this is cleared in onChange rather than by an effect on the value:
  // picking a row also changes the value, and an effect would reopen the panel
  // the pick just closed.
  const [dismissed, setDismissed] = useState(false);
  const area = useRef<HTMLTextAreaElement>(null);
  const mirror = useRef<HTMLDivElement>(null);
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

  // A fork in front of a prompt hands that prompt back, and the point of handing
  // it back is to change it: the caret goes to the end so the reader continues
  // the sentence instead of selecting and retyping it. The palette is dismissed
  // on the way in — a message that happens to start with a slash would otherwise
  // open a command list over the sentence the reader is about to edit.
  useEffect(() => {
    if (!seed) return;
    setValue(seed.text);
    setDismissed(true);
    const node = area.current;
    if (!node) return;
    node.focus();
    window.requestAnimationFrame(() => node.setSelectionRange(seed.text.length, seed.text.length));
  }, [seed]);

  // A turn in flight cannot run a command — send is refused while disabled — so
  // offering one would be a panel whose rows do nothing.
  const palette = disabled || dismissed || plain ? null : matches(value);
  const command = plain ? null : leadingCommand(value);

  const send = () => {
    const text = value.trim();
    if (!text || disabled) return;
    onSend(text);
    setValue("");
    setDismissed(false);
  };

  // pick completes the line with the command's name and leaves the caret at its
  // end, ready for an argument or for Enter.
  const pick = (name: string) => {
    setValue(name);
    setDismissed(true);
    const node = area.current;
    if (!node) return;
    node.focus();
    window.requestAnimationFrame(() => node.setSelectionRange(name.length, name.length));
  };

  return (
    <div className="composer">
      <div className="composer__inner">
        {palette ? (
          <CommandPalette items={palette} onPick={pick} onClose={() => setDismissed(true)} />
        ) : null}
        <div className="composer__box">
          <div className="composer__field">
            {command ? (
              // The paint layer. Its text is transparent — the glyphs the reader
              // sees are the textarea's own — so the two never disagree about
              // anything except where the highlight box goes. It is the same box
              // with the same metrics, and it scrolls with the textarea.
              <div className="composer__mirror" aria-hidden="true" ref={mirror}>
                {command.lead}
                <mark className="composer__command">{command.name}</mark>
                {command.rest}
              </div>
            ) : null}
            <textarea
              ref={area}
              rows={1}
              value={value}
              placeholder={placeholder ?? "问点什么…  / 看命令"}
              autoFocus
              onChange={(event) => {
                setValue(event.target.value);
                setDismissed(false);
              }}
              onScroll={(event) => {
                // A long draft scrolls inside the box once it passes the
                // ceiling; the paint has to follow it up or the highlight stays
                // behind on the first line.
                const layer = mirror.current;
                if (layer) layer.scrollTop = event.currentTarget.scrollTop;
              }}
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
                if (event.key === "Escape" && palette) {
                  event.preventDefault();
                  setDismissed(true);
                  return;
                }
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
          </div>
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
