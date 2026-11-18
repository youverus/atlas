import type { Command } from "../lib/commands";

// The command palette, drawn above the input.
//
// It is a list of buttons rather than a listbox because every row is clicked:
// the reader picks a command and the name lands in the draft, where it can be
// edited before it runs. That is the terminal's behaviour too — the palette
// completes the line, it does not fire it.
//
// Rows take the mousedown themselves so the textarea keeps focus and the caret
// stays where it was; without that, clicking a row would blur the box and the
// insertion would land with the reader's place in the line already lost.
export function CommandPalette({
  items,
  onPick,
  onClose,
}: {
  items: Command[];
  onPick: (name: string) => void;
  onClose: () => void;
}) {
  return (
    <div
      className="palette"
      role="group"
      aria-label="命令"
      onKeyDown={(event) => {
        if (event.key === "Escape") {
          event.preventDefault();
          onClose();
        }
      }}
    >
      {items.length === 0 ? (
        <div className="palette__empty">没有这个命令</div>
      ) : (
        items.map((command) => (
          <button
            key={command.name}
            type="button"
            className="palette__row"
            onMouseDown={(event) => event.preventDefault()}
            onClick={() => onPick(command.name)}
          >
            <span className="palette__name">{command.name}</span>
            {command.usage ? <span className="palette__usage">{command.usage}</span> : null}
            <span className="palette__about">{command.summary}</span>
          </button>
        ))
      )}
      <div className="palette__foot">
        窗口能跑的命令。新对话、归档、恢复在左侧有按钮；/export 与 /resume 还没接进来。Esc 关掉这一栏
      </div>
    </div>
  );
}
