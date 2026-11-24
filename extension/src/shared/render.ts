// 保守的 Markdown 渲染：先整体转义，再白名单式恢复少量结构。
// 扩展面板只承载对话正文，数学公式与表格留给桌面端（M4 再评估引入 KaTeX）。

export function renderMarkdown(source: string): string {
  const blocks: string[] = [];
  const lines = source.replace(/\r\n/g, "\n").split("\n");
  let index = 0;
  while (index < lines.length) {
    const line = lines[index] ?? "";
    if (line.startsWith("```")) {
      const body: string[] = [];
      index += 1;
      while (index < lines.length && !(lines[index] ?? "").startsWith("```")) {
        body.push(lines[index] ?? "");
        index += 1;
      }
      index += 1; // 跳过收尾的 ```（缺失时到头自然停止）
      blocks.push(`<pre><code>${escapeHTML(body.join("\n"))}</code></pre>`);
      continue;
    }
    const heading = /^(#{1,6})\s+(.*)$/.exec(line);
    if (heading) {
      const level = heading[1]?.length ?? 6;
      blocks.push(`<h${level}>${inline(heading[2] ?? "")}</h${level}>`);
      index += 1;
      continue;
    }
    const bullet = /^\s*[-*]\s+(.*)$/.exec(line);
    if (bullet) {
      const items: string[] = [];
      while (index < lines.length) {
        const match = /^\s*[-*]\s+(.*)$/.exec(lines[index] ?? "");
        if (!match) break;
        items.push(`<li>${inline(match[1] ?? "")}</li>`);
        index += 1;
      }
      blocks.push(`<ul>${items.join("")}</ul>`);
      continue;
    }
    const ordered = /^\s*\d+[.)]\s+(.*)$/.exec(line);
    if (ordered) {
      const items: string[] = [];
      while (index < lines.length) {
        const match = /^\s*\d+[.)]\s+(.*)$/.exec(lines[index] ?? "");
        if (!match) break;
        items.push(`<li>${inline(match[1] ?? "")}</li>`);
        index += 1;
      }
      blocks.push(`<ol>${items.join("")}</ol>`);
      continue;
    }
    if (line.trim() === "") {
      index += 1;
      continue;
    }
    const paragraph: string[] = [];
    while (index < lines.length && (lines[index] ?? "").trim() !== "" &&
      !/^(#{1,6})\s|^\s*[-*]\s|^\s*\d+[.)]\s|^```/.test(lines[index] ?? "")) {
      paragraph.push(lines[index] ?? "");
      index += 1;
    }
    blocks.push(`<p>${inline(paragraph.join("\n"))}</p>`);
  }
  return blocks.join("");
}

// 行内结构：code → 链接 → 加粗 → 斜体。链接只允许 http(s)，其余协议
// （javascript: 等）不生成锚点，防止面板内点击执行任何东西。
function inline(text: string): string {
  const escaped = escapeHTML(text);
  const withCode = escaped.replace(/`([^`\n]+)`/g, (_all, body: string) => `<code>${body}</code>`);
  const withLinks = withCode.replace(/\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)/g, (_all, label: string, url: string) =>
    `<a href="${url}" target="_blank" rel="noreferrer noopener">${label}</a>`);
  const withBold = withLinks.replace(/\*\*([^*\n]+)\*\*/g, "<strong>$1</strong>");
  return withBold.replace(/(^|[^*])\*([^*\n]+)\*(?!\*)/g, "$1<em>$2</em>");
}

function escapeHTML(text: string): string {
  return text
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}
