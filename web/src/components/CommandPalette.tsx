import { useEffect, useRef } from "react";
import type { PaletteItem } from "../lib/commands";

// Draft completions above the input. Mention navigation leaves focus in the
// textarea so keyboard and mouse selection both preserve the replacement range.
export function CommandPalette({
  id,
  items,
  kind,
  selectedIndex,
  emptyLabel,
  onPick,
  onClose,
}: {
  id: string;
  items: PaletteItem[];
  kind: "command" | "mention";
  selectedIndex: number | null;
  emptyLabel: string;
  onPick: (index: number) => void;
  onClose: () => void;
}) {
  const selected = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    selected.current?.scrollIntoView({ block: "nearest" });
  }, [selectedIndex]);

  const mentions = kind === "mention";
  return (
    <div
      id={id}
      className="palette"
      data-kind={kind}
      role={mentions ? "listbox" : "group"}
      aria-label={mentions ? "@ 候选" : "命令"}
      onKeyDown={(event) => {
        if (event.key === "Escape") {
          event.preventDefault();
          onClose();
        }
      }}
    >
      {items.length === 0 ? (
        <div className="palette__empty" role="status">{emptyLabel}</div>
      ) : (
        items.map((item, index) => (
          <button
            key={`${item.name}-${item.summary}`}
            id={`${id}-${index}`}
            ref={selectedIndex === index ? selected : undefined}
            type="button"
            className="palette__row"
            role={mentions ? "option" : undefined}
            tabIndex={mentions ? -1 : undefined}
            aria-selected={mentions ? selectedIndex === index : undefined}
            title={mentions ? item.name : undefined}
            onMouseDown={(event) => event.preventDefault()}
            onClick={() => onPick(index)}
          >
            <span className="palette__name">{item.name}</span>
            {item.usage ? <span className="palette__usage">{item.usage}</span> : null}
            <span className="palette__about">{item.summary}</span>
          </button>
        ))
      )}
      {kind === "command" ? (
        <div className="palette__foot">
          窗口能跑的命令。新对话、归档、恢复在左侧有按钮；/export 与 /resume 还没接进来。Esc 关掉这一栏
        </div>
      ) : null}
    </div>
  );
}
