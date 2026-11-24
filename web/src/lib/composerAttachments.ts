import type { AttachmentPayload } from "../api";

/** A file type accepted by the composer attachment picker. */
export type AttachmentKind = AttachmentPayload["kind"];

/** A validated file with the metadata used by the composer chip. */
export interface StagedAttachment extends AttachmentPayload {
  id: string;
  size: number;
  previewUrl?: string;
}

export const MAX_ATTACHMENTS = 8;
const MAX_IMAGE_BYTES = 5 * 1024 * 1024;
const MAX_PDF_BYTES = 15 * 1024 * 1024;
const MAX_TEXT_BYTES = 512 * 1024;

const IMAGE_TYPES = new Set(["image/png", "image/jpeg", "image/gif", "image/webp"]);
const IMAGE_EXT_RE = /\.(png|jpe?g|gif|webp)$/i;
const TEXT_EXT_RE =
  /\.(txt|md|markdown|json|jsonl|csv|tsv|log|yaml|yml|toml|xml|html?|css|scss|less|js|mjs|cjs|jsx|ts|tsx|go|py|rs|java|kt|c|h|cc|cpp|hpp|sh|bash|zsh|sql|env|ini|conf|cfg)$/i;

/** Detect file drags even before their payload is available. */
export function hasDraggedFiles(transfer: DataTransfer): boolean {
  return Array.from(transfer.types).includes("Files") || Array.from(transfer.items).some((item) => item.kind === "file");
}

/** Read clipboard or drop files once, excluding directory entries. */
export function transferredFiles(transfer: DataTransfer) {
  const files: File[] = [];
  let hasDirectory = false;
  for (const item of Array.from(transfer.items)) {
    if (item.kind !== "file") continue;
    if (item.webkitGetAsEntry?.()?.isDirectory) {
      hasDirectory = true;
      continue;
    }
    const file = item.getAsFile();
    if (file) files.push(file);
  }
  const fileList = Array.from(transfer.files);
  return { files: hasDirectory || fileList.length === 0 ? files : fileList, hasDirectory };
}

function attachmentKind(file: File): AttachmentKind {
  if (IMAGE_TYPES.has(file.type) || IMAGE_EXT_RE.test(file.name)) return "image";
  if (file.type === "application/pdf" || /\.pdf$/i.test(file.name)) return "pdf";
  return "text";
}

/** Validate and convert a selected, dropped, or pasted file to a draft attachment. */
export async function readStagedFile(file: File, kind = attachmentKind(file)): Promise<StagedAttachment> {
  const id = `${Date.now()}-${Math.random().toString(36).slice(2, 9)}`;
  if (kind === "image") {
    if (!IMAGE_TYPES.has(file.type) && !IMAGE_EXT_RE.test(file.name)) {
      throw new Error(`「${file.name}」不是受支持的图片格式（仅支持 PNG/JPG/GIF/WebP）`);
    }
    if (file.size > MAX_IMAGE_BYTES) {
      throw new Error(`图片「${file.name}」超过 ${formatBytes(MAX_IMAGE_BYTES)} 上限`);
    }
    const bytes = new Uint8Array(await file.arrayBuffer());
    const base64 = bytesToBase64(bytes);
    const mimeType = IMAGE_TYPES.has(file.type) ? file.type : guessImageMime(file.name);
    return {
      id,
      kind: "image",
      name: file.name,
      mimeType,
      size: file.size,
      data: base64,
      previewUrl: `data:${mimeType};base64,${base64}`,
    };
  }

  if (kind === "pdf") {
    if (file.type !== "application/pdf" && !/\.pdf$/i.test(file.name)) {
      throw new Error(`「${file.name}」不是 PDF 文件`);
    }
    if (file.size > MAX_PDF_BYTES) {
      throw new Error(`PDF「${file.name}」超过 ${formatBytes(MAX_PDF_BYTES)} 上限`);
    }
    const bytes = new Uint8Array(await file.arrayBuffer());
    return {
      id,
      kind: "pdf",
      name: file.name,
      mimeType: "application/pdf",
      size: file.size,
      data: bytesToBase64(bytes),
    };
  }

  if (file.size > MAX_TEXT_BYTES) {
    throw new Error(`文本文件「${file.name}」超过 ${formatBytes(MAX_TEXT_BYTES)} 上限`);
  }
  const bytes = new Uint8Array(await file.arrayBuffer());
  if (bytes.includes(0)) {
    throw new Error(`「${file.name}」包含二进制内容，暂仅支持纯文本文件`);
  }
  if (!file.type.startsWith("text/") && !TEXT_EXT_RE.test(file.name) && file.type !== "application/json") {
    // Verify strict UTF-8 for extensionless text files (e.g. README, Makefile).
    try {
      new TextDecoder("utf-8", { fatal: true }).decode(bytes);
    } catch {
      throw new Error(`「${file.name}」不是受支持的 UTF-8 文本文件`);
    }
  }
  let text: string;
  try {
    text = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
  } catch {
    throw new Error(`「${file.name}」不是有效的 UTF-8 文本编码`);
  }
  return {
    id,
    kind: "text",
    name: file.name,
    mimeType: file.type || "text/plain",
    size: file.size,
    data: text,
  };
}

function bytesToBase64(bytes: Uint8Array): string {
  let binary = "";
  const chunk = 0x8000;
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunk));
  }
  return btoa(binary);
}

function guessImageMime(name: string): string {
  const lower = name.toLowerCase();
  if (lower.endsWith(".jpg") || lower.endsWith(".jpeg")) return "image/jpeg";
  if (lower.endsWith(".gif")) return "image/gif";
  if (lower.endsWith(".webp")) return "image/webp";
  return "image/png";
}

/** Format the byte count used by attachment chips and validation errors. */
export function formatBytes(size: number): string {
  if (size < 1024) return `${size} B`;
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`;
  return `${(size / (1024 * 1024)).toFixed(1)} MB`;
}
