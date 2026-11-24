import type { MouseEvent } from "react";
import Markdown from "react-markdown";
import rehypeKatex from "rehype-katex";
import remarkGfm from "remark-gfm";
import remarkMath from "remark-math";

import { api } from "../api";
import { remarkGuardInlineMath } from "../lib/math";

function openMarkdownLink(event: MouseEvent<HTMLAnchorElement>, href?: string) {
  if (!href || !/^https?:\/\//i.test(href)) return;
  event.preventDefault();
  void api
    .browserOpen(href)
    .then((tab) => {
      window.dispatchEvent(new CustomEvent("atlas:browser-opened", { detail: { tab } }));
    })
    .catch(() => {
      window.open(href, "_blank", "noopener,noreferrer");
    });
}

// A model's answer is markdown, and GFM is the dialect it writes without being
// asked: tables and strikethrough are common enough that a renderer without
// them shows the source instead of the answer.
//
// Formulas are the same argument one step further. A math question's answer is
// the formula, so a renderer that cannot draw it shows `\begin{cases}` and
// `\mu(n)` to the reader. remark-math cuts the math out of the paragraph and
// rehype-katex draws it; the guard in between keeps single dollars from turning
// prices into formulas (see lib/math.ts for the measured rule).
export function MarkdownBody({ text }: { text: string }) {
  return (
    <div className="markdown">
      <Markdown
        remarkPlugins={[remarkGfm, remarkMath, remarkGuardInlineMath]}
        rehypePlugins={[[rehypeKatex, { strict: false }]]}
        components={{
          a: ({ node: _node, ...props }) => (
            <a
              {...props}
              target="_blank"
              rel="noreferrer noopener"
              title={props.href ? `在受控浏览器中打开: ${props.href}` : undefined}
              onClick={(event) => openMarkdownLink(event, props.href)}
            />
          ),
        }}
      >
        {text}
      </Markdown>
    </div>
  );
}
