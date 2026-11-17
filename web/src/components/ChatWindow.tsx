import { useEffect, useRef } from "react";
import type { Block } from "../lib/fold";
import { MarkdownBody } from "./MarkdownBody";
import { ThinkingBlock } from "./ThinkingBlock";
import { ToolCard } from "./ToolCard";

// The transcript.
//
// Auto-scroll follows the end of the content only while the reader is already
// there: scrolling up to read something must not be undone by the next token,
// which is the single most common way a streaming window becomes unusable.
export function ChatWindow({ blocks, running }: { blocks: Block[]; running: boolean }) {
  const scroll = useRef<HTMLDivElement>(null);
  const pinned = useRef(true);

  useEffect(() => {
    const node = scroll.current;
    if (!node || !pinned.current) return;
    node.scrollTop = node.scrollHeight;
  }, [blocks]);

  const onScroll = () => {
    const node = scroll.current;
    if (!node) return;
    const distance = node.scrollHeight - node.scrollTop - node.clientHeight;
    pinned.current = distance < 48;
  };

  if (blocks.length === 0) {
    return (
      <div className="chat__scroll" ref={scroll}>
        <div className="chat__empty">
          <h1>atlas</h1>
          <p>问一个问题开始，或从左侧选一段对话。</p>
        </div>
      </div>
    );
  }

  return (
    <div className="chat__scroll" ref={scroll} onScroll={onScroll}>
      <div className="chat__column">
        {group(blocks).map((turn, index) => (
          <div className="turn" key={index}>
            {turn.map((block) => (
              <BlockView key={block.id} block={block} />
            ))}
          </div>
        ))}
        {running ? <span className="sr-only">running</span> : null}
      </div>
    </div>
  );
}

function BlockView({ block }: { block: Block }) {
  switch (block.role) {
    case "user":
      return <div className="bubble">{block.text}</div>;
    case "answer":
      return <MarkdownBody text={block.text} />;
    case "thinking":
      return <ThinkingBlock block={block} />;
    case "tool":
      return <ToolCard block={block} />;
    case "error":
      return <div className="notice notice--error">{block.text}</div>;
    default:
      return (
        <div className="notice">
          <span className="notice__rule" />
          <span>{block.text}</span>
          <span className="notice__rule" />
        </div>
      );
  }
}

// group starts a new turn at every prompt. A notice belongs to the turn above
// it, which is why only "user" opens one.
function group(blocks: Block[]): Block[][] {
  const turns: Block[][] = [];
  for (const block of blocks) {
    if (block.role === "user" || turns.length === 0) {
      turns.push([]);
    }
    turns[turns.length - 1].push(block);
  }
  return turns;
}
