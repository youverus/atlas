import type { ArtifactRef } from "../api";

export interface ParsedCsv {
  headers: string[];
  rows: string[][];
}

const LANGUAGE_EXTENSIONS: Record<string, string> = {
  bash: "sh",
  c: "c",
  cpp: "cpp",
  "c++": "cpp",
  css: "css",
  go: "go",
  golang: "go",
  html: "html",
  java: "java",
  javascript: "js",
  js: "js",
  json: "json",
  jsx: "jsx",
  markdown: "md",
  md: "md",
  py: "py",
  python: "py",
  rs: "rs",
  rust: "rs",
  sh: "sh",
  shell: "sh",
  sql: "sql",
  svg: "svg",
  toml: "toml",
  ts: "ts",
  tsx: "tsx",
  typescript: "ts",
  xml: "xml",
  yaml: "yaml",
  yml: "yaml",
};

const ARTIFACT_CSP =
  "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; media-src data: blob:; connect-src 'none'; frame-src 'none'; object-src 'none'; form-action 'none'; base-uri 'none';";

export function artifactKindLabel(kind: string, language?: string): string {
  switch (kind) {
    case "html":
      return "HTML";
    case "svg":
      return "SVG";
    case "markdown":
      return "Markdown";
    case "csv":
      return "CSV";
    case "code": {
      const lang = language?.trim();
      return lang ? lang.toUpperCase() : "CODE";
    }
    default:
      return kind.toUpperCase() || "ARTIFACT";
  }
}

export function artifactExtension(kind: string, language?: string): string {
  switch (kind) {
    case "html":
      return "html";
    case "svg":
      return "svg";
    case "markdown":
      return "md";
    case "csv":
      return "csv";
    case "code": {
      const key = language?.trim().toLowerCase() ?? "";
      return LANGUAGE_EXTENSIONS[key] ?? "txt";
    }
    default:
      return "txt";
  }
}

export function defaultExportFilename(id: string, kind: string, language?: string): string {
  const safeId = id.trim() || "artifact";
  return `artifacts/${safeId}.${artifactExtension(kind, language)}`;
}

export function formatArtifactBytes(bytes?: number): string {
  if (bytes === undefined || bytes < 0) return "";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(2)} MB`;
}

/** Deduplicate artifact receipts produced within one turn, keeping latest per ID. */
export function collectTurnArtifacts(
  blocks: readonly { error?: boolean; artifact?: ArtifactRef }[],
): ArtifactRef[] {
  const ordered: ArtifactRef[] = [];
  const indexById = new Map<string, number>();
  for (const block of blocks) {
    if (block.error || !block.artifact?.id) continue;
    const prevIdx = indexById.get(block.artifact.id);
    if (prevIdx !== undefined) {
      ordered[prevIdx] = block.artifact;
    } else {
      indexById.set(block.artifact.id, ordered.length);
      ordered.push(block.artifact);
    }
  }
  return ordered;
}

/** Pure RFC-4180 CSV parser handling quotes, escaped double-quotes, and embedded newlines. */
export function parseCsv(text: string): ParsedCsv {
  const normalized = text.startsWith("\uFEFF") ? text.slice(1) : text;
  if (!normalized) return { headers: [], rows: [] };

  const records: string[][] = [];
  let row: string[] = [];
  let field = "";
  let inQuotes = false;

  for (let i = 0; i < normalized.length; i += 1) {
    const ch = normalized[i];
    if (inQuotes) {
      if (ch === '"') {
        if (normalized[i + 1] === '"') {
          field += '"';
          i += 1;
        } else {
          inQuotes = false;
        }
      } else {
        field += ch;
      }
      continue;
    }
    if (ch === '"' && field.length === 0) {
      inQuotes = true;
    } else if (ch === ",") {
      row.push(field);
      field = "";
    } else if (ch === "\r" || ch === "\n") {
      if (ch === "\r" && normalized[i + 1] === "\n") {
        i += 1;
      }
      row.push(field);
      records.push(row);
      row = [];
      field = "";
    } else {
      field += ch;
    }
  }

  if (field.length > 0 || row.length > 0) {
    row.push(field);
    records.push(row);
  }

  // Drop empty trailing lines produced by trailing newlines.
  while (records.length > 0 && records[records.length - 1].every((cell) => cell === "")) {
    records.pop();
  }
  if (records.length === 0) return { headers: [], rows: [] };

  const headers = records[0];
  let width = headers.length;
  for (let r = 1; r < records.length; r += 1) {
    if (records[r].length > width) width = records[r].length;
  }
  const paddedHeaders = headers.slice();
  while (paddedHeaders.length < width) paddedHeaders.push("");

  const rows = records.slice(1).map((rec) => {
    if (rec.length === width) return rec;
    const copy = rec.slice();
    while (copy.length < width) copy.push("");
    return copy;
  });

  return { headers: paddedHeaders, rows };
}

export function sortCsvRows(
  rows: readonly string[][],
  columnIndex: number,
  direction: "asc" | "desc",
): string[][] {
  const factor = direction === "asc" ? 1 : -1;
  return rows.slice().sort((a, b) => {
    const left = (a[columnIndex] ?? "").trim();
    const right = (b[columnIndex] ?? "").trim();
    if (left === right) return 0;
    if (left === "") return 1;
    if (right === "") return -1;
    const leftNum = Number(left);
    const rightNum = Number(right);
    if (!Number.isNaN(leftNum) && !Number.isNaN(rightNum) && Number.isFinite(leftNum) && Number.isFinite(rightNum)) {
      return (leftNum - rightNum) * factor;
    }
    return left.localeCompare(right, undefined, { numeric: true, sensitivity: "base" }) * factor;
  });
}

/**
 * Wrap untrusted HTML artifact source in a strict CSP and theme-synchronized
 * stylesheet before loading it inside `<iframe sandbox="allow-scripts">`.
 */
export function wrapArtifactHtml(html: string, dark: boolean): string {
  const themeVars = dark
    ? "--bg:#161616;--surface:#202022;--fg:#f3f3f3;--muted:#ababab;--line:rgba(255,255,255,0.12);--accent:#3b82f6;"
    : "--bg:#ffffff;--surface:#f7f7f7;--fg:#171717;--muted:#5c5c5c;--line:#e5e5e5;--accent:#2563eb;";
  const colorScheme = dark ? "dark" : "light";
  const preamble = [
    `<meta charset="utf-8">`,
    `<meta http-equiv="Content-Security-Policy" content="${ARTIFACT_CSP}">`,
    `<meta name="color-scheme" content="${colorScheme}">`,
    `<style id="atlas-artifact-theme">:root{${themeVars}color-scheme:${colorScheme};}*,*::before,*::after{box-sizing:border-box;}html,body{margin:0;padding:0;min-height:100%;}body{padding:20px;background:var(--bg);color:var(--fg);font-family:-apple-system,BlinkMacSystemFont,"SF Pro Text","Segoe UI",system-ui,sans-serif;line-height:1.55;}</style>`,
  ].join("");

  const headMatch = /<head(\s[^>]*)?>/i.exec(html);
  const bodyMatch = /<body(\s[^>]*)?>/i.exec(html);
  if (headMatch && (!bodyMatch || headMatch.index < bodyMatch.index)) {
    const insertAt = headMatch.index + headMatch[0].length;
    return html.slice(0, insertAt) + preamble + html.slice(insertAt);
  }

  const htmlMatch = /<html(\s[^>]*)?>/i.exec(html);
  if (htmlMatch && (!bodyMatch || htmlMatch.index < bodyMatch.index)) {
    const insertAt = htmlMatch.index + htmlMatch[0].length;
    return html.slice(0, insertAt) + `<head>${preamble}</head>` + html.slice(insertAt);
  }

  return `<!doctype html><html lang="zh-CN"><head>${preamble}</head><body>${html}</body></html>`;
}
