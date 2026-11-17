import Markdown from "react-markdown";
import remarkGfm from "remark-gfm";

// A model's answer is markdown, and GFM is the dialect it writes without being
// asked: tables and strikethrough are common enough that a renderer without
// them shows the source instead of the answer.
export function MarkdownBody({ text }: { text: string }) {
  return (
    <div className="markdown">
      <Markdown
        remarkPlugins={[remarkGfm]}
        components={{
          a: ({ node: _node, ...props }) => (
            <a {...props} target="_blank" rel="noreferrer noopener" />
          ),
        }}
      >
        {text}
      </Markdown>
    </div>
  );
}
