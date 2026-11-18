import type { Root } from "mdast";
import type { Transformer } from "unified";

// Math reaches the window as ordinary text, and markdown has no idea what it is.
// Two things go wrong without remark-math: `$$...$$` arrives as a paragraph of
// dollar signs, and the `\\` that ends a line inside `cases` loses one backslash
// to markdown's escape rule before any renderer sees it. Cutting math out of the
// text first is what keeps the source verbatim, so remark-math runs before
// anything else touches the paragraph.
//
// Single dollars need a guard on top of that. The character that opens
// `$p \mid n$` also opens a price, and remark-math will happily turn
// "Pricing: $8 / MTok for input, $4 / MTok for output." into the formula
// `8 / MTok for input, `. Over the 156 answers stored in this machine's session
// history there were 54 single-dollar candidates: 26 real formulas and 28
// prices. Every one of the 26 carried a TeX command, a superscript or a
// subscript; none of the 28 did. So that is the rule, and a candidate without
// one of those three marks goes back to being the text the reader typed.
const MATH_MARKER = /\\[a-zA-Z]+|\^|_/;

export function looksLikeMath(value: string): boolean {
  return MATH_MARKER.test(value);
}

// The node shapes this plugin touches, kept structural so the file does not
// depend on mdast-util-math's type augmentation being in scope.
type Node = {
  type: string;
  value?: string;
  data?: unknown;
  position?: { start?: { offset?: number } };
  children?: Node[];
};

// A formula written with double dollars on one line comes back from remark-math
// as inline math. It is still math — the reader wrote the delimiters that mean
// "formula" — so it is exempt from the guard, and the source offsets are the
// only place that distinction survives.
function writtenWithDoubleDollars(source: string, node: Node): boolean {
  const offset = node.position?.start?.offset;
  if (offset === undefined) return false;
  return source.slice(offset, offset + 2) === "$$";
}

export function remarkGuardInlineMath(): Transformer<Root, Root> {
  return (tree, file) => {
    guard(tree as unknown as Node, String(file));
  };
}

function guard(node: Node, source: string): void {
  if (node.type === "inlineMath" && typeof node.value === "string") {
    if (!writtenWithDoubleDollars(source, node) && !looksLikeMath(node.value)) {
      // Back to the characters the reader typed: not a formula, so nothing is
      // rendered and nothing is lost.
      node.type = "text";
      node.value = "$" + node.value + "$";
      // remark-math also attaches a hast hint (`hName: "code"` plus the
      // `language-math` class), and mdast-util-to-hast applies it whatever the
      // node type says. Without this line the "text" node still reaches the
      // page as <code class="language-math">, and rehype-katex renders the
      // price after all.
      node.data = undefined;
    }
  }
  for (const child of node.children ?? []) guard(child, source);
}
