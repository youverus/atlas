import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { ChangeEvent, ReactNode } from "react";
import { api, type AttachmentPayload, type DirListing } from "../api";
import { formatBytes, hasDraggedFiles, MAX_ATTACHMENTS, readStagedFile, transferredFiles, type AttachmentKind, type StagedAttachment } from "../lib/composerAttachments";
import { inPromptText, leadingCommand, matches, roleMatches, type PaletteItem } from "../lib/commands";
import { matchingPaths, pathInsertion, pathMention, type PathCompletionScope } from "../lib/pathCompletion";
import { CommandPalette } from "./CommandPalette";

interface DirectoryRequest {
  fileScope: PathCompletionScope;
  directory: string;
}

interface CompletionItem extends PaletteItem {
  start: number;
  end: number;
  path?: ReturnType<typeof pathInsertion>;
  isDir?: boolean;
}

// The input. Enter sends, Shift+Enter breaks the line, and the box grows with
// the draft up to a ceiling: a prompt that scrolls inside a 44px box is a
// prompt the reader cannot proofread.
export function Composer({
  disabled,
  locked = false,
  allowAttachments = false,
  fileScope,
  onSend,
  onStop,
  stopping = false,
  onDraftChange,
  plain,
  placeholder,
  footer,
  footerRight,
  footerOverlay,
}: {
  disabled: boolean;
  locked?: boolean;
  allowAttachments?: boolean;
  fileScope?: PathCompletionScope;
  onSend: (text: string, attachments?: AttachmentPayload[]) => void;
  onStop?: () => void;
  stopping?: boolean;
  onDraftChange?: (hasDraft: boolean) => void;
  plain?: boolean;
  placeholder?: string;
  footer?: ReactNode;
  footerRight?: ReactNode;
  footerOverlay?: ReactNode;
}) {
  const [value, setValue] = useState("");
  const [caret, setCaret] = useState<number | null>(0);
  const [focused, setFocused] = useState(false);
  const [dismissed, setDismissed] = useState<number | null>(null);
  const [activeCandidate, setActiveCandidate] = useState<{ index: number; request: DirectoryRequest | null } | null>(null);
  const [compositionActive, setCompositionActive] = useState(false);
  const [attachMenuOpen, setAttachMenuOpen] = useState(false);
  const [attachments, setAttachments] = useState<StagedAttachment[]>([]);
  const [attachError, setAttachError] = useState("");
  const [stagingAttachments, setStagingAttachments] = useState(false);
  const [draggingFiles, setDraggingFiles] = useState(false);
  const paletteId = useId();
  const selectedCaret = useRef<number | null>(null);
  const stagedFiles = useRef<StagedAttachment[]>([]);
  const staging = useRef({ queue: Promise.resolve(), pending: 0 });
  const dragDepth = useRef(0);
  const area = useRef<HTMLTextAreaElement>(null);
  const mirror = useRef<HTMLDivElement>(null);
  const attachWrapRef = useRef<HTMLDivElement>(null);
  const imageInputRef = useRef<HTMLInputElement>(null);
  const pdfInputRef = useRef<HTMLInputElement>(null);
  const textInputRef = useRef<HTMLInputElement>(null);
  const composing = useRef(false);
  const justComposed = useRef(false);

  useEffect(() => {
    const node = area.current;
    if (!node) return;
    node.style.height = "auto";
    node.style.height = `${Math.min(node.scrollHeight, 200)}px`;
  }, [value]);

  useEffect(() => {
    if (!attachMenuOpen) return;
    const onPointerDown = (event: MouseEvent) => {
      if (attachWrapRef.current && !attachWrapRef.current.contains(event.target as Node)) {
        setAttachMenuOpen(false);
      }
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setAttachMenuOpen(false);
      }
    };
    document.addEventListener("mousedown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("mousedown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [attachMenuOpen]);

  const blocked = disabled || locked;
  const hasAttachments = attachments.length > 0 || stagingAttachments;
  const hasDraft = value.length > 0 || hasAttachments || compositionActive;
  useEffect(() => onDraftChange?.(hasDraft), [hasDraft, onDraftChange]);

  const trimmedStart = value.trimStart();
  const command = plain || locked || hasAttachments ? null : leadingCommand(value);
  const canPick = !blocked && !compositionActive;
  const slashItems = canPick && !plain && !hasAttachments ? matches(value) : null;
  const rawMention = canPick && focused && !slashItems && caret !== null ? pathMention(value, caret) : null;
  const mention = rawMention && inPromptText(value.slice(0, rawMention.start)) ? rawMention : null;
  const rawRole = canPick && focused && !plain && !trimmedStart.startsWith("/") && caret !== null
    ? roleMatches(value, caret)
    : null;
  const role = rawRole && (!mention || rawRole.start === mention.start) ? rawRole : null;
  const directory = mention?.start === dismissed ? undefined : mention?.directory;
  const request = useMemo<DirectoryRequest | null>(
    () => fileScope && directory !== undefined ? { fileScope, directory } : null,
    [fileScope, directory],
  );
  const [result, setResult] = useState<{ request: DirectoryRequest; listing?: DirListing }>();

  useEffect(() => {
    if (!request) return;
    const { fileScope, directory } = request;
    const controller = new AbortController();
    void api.files(directory.replace(/\/$/, ""), controller.signal).then((listing) => {
      if (controller.signal.aborted) return;
      const expectedRoot = fileScope.directory?.replace(/\/+$/, "");
      const actualRoot = listing.root?.replace(/\/+$/, "");
      const wrongRoot = expectedRoot !== undefined && actualRoot !== undefined && expectedRoot !== actualRoot;
      setResult({ request, listing: wrongRoot ? undefined : listing });
    }).catch(() => {
      if (!controller.signal.aborted) setResult({ request });
    });
    return () => controller.abort();
  }, [request]);

  const loadingPaths = Boolean(request && result?.request !== request);
  const listing = request && result?.request === request ? result.listing : undefined;
  const pathItems = mention ? matchingPaths(listing, mention.prefix).map((entry) => {
    const { start, end } = mention;
    // A root path can have the same spelling as a bare subagent role.
    const bare = `@${entry.path}`;
    const reserved = !entry.isDir && roleMatches(bare, bare.length)?.items.some((item) => item.name === bare);
    const path = pathInsertion(entry.path, entry.isDir, reserved);
    return { name: path.text, summary: entry.isDir ? "目录" : "文件", start, end, path, isDir: entry.isDir };
  }) : [];
  const slashStart = value.length - trimmedStart.length;
  const paletteStart = slashItems ? slashStart : role?.start ?? (request ? mention?.start : undefined);
  const paletteKind = slashItems ? "command" : "mention";
  const showPalette = paletteStart !== undefined && paletteStart !== dismissed;
  const items: CompletionItem[] = slashItems
    ? slashItems.map((item) => ({ ...item, start: slashStart, end: value.length }))
    : [...(role ? role.items.map((item) => ({ ...item, start: role.start, end: role.end })) : []), ...pathItems];
  const selectedIndex = activeCandidate?.request === request && activeCandidate.index < items.length
    ? activeCandidate.index
    : null;

  const syncCaret = (node: HTMLTextAreaElement) => {
    const next = node.selectionStart === node.selectionEnd ? node.selectionStart : null;
    if (next !== caret) setActiveCandidate(null);
    setCaret(next);
  };

  useLayoutEffect(() => {
    const position = selectedCaret.current;
    if (position === null) return;
    selectedCaret.current = null;
    area.current?.setSelectionRange(position, position);
  });

  useEffect(() => () => {
    staging.current = { queue: Promise.resolve(), pending: 0 };
  }, []);

  const updateAttachments = (next: StagedAttachment[]) => {
    stagedFiles.current = next;
    setAttachments(next);
  };

  const send = () => {
    const text = value.trim();
    const files = stagedFiles.current;
    if ((!text && files.length === 0) || blocked || staging.current.pending > 0 || composing.current) return;
    const payloads =
      files.length > 0
        ? files.map(({ kind, name, mimeType, data }) => ({
            kind,
            name,
            ...(mimeType ? { mimeType } : {}),
            data,
          }))
        : undefined;
    onSend(text, payloads);
    setValue("");
    updateAttachments([]);
    setAttachError("");
    setAttachMenuOpen(false);
    setCaret(0);
    setDismissed(null);
    setActiveCandidate(null);
  };

  const pick = (index: number) => {
    const item = items[index];
    if (!showPalette || !item || composing.current || justComposed.current) return;
    const { start, end, name, path, isDir } = item;
    const next = value.slice(0, start) + name + value.slice(end);
    const position = start + (path?.caretOffset ?? name.length);
    area.current?.focus();
    selectedCaret.current = position;
    setValue(next);
    setCaret(position);
    setDismissed(isDir ? null : start);
    setActiveCandidate(null);
  };

  const triggerPicker = (kind: AttachmentKind) => {
    setAttachMenuOpen(false);
    setAttachError("");
    if (kind === "image") imageInputRef.current?.click();
    else if (kind === "pdf") pdfInputRef.current?.click();
    else textInputRef.current?.click();
  };

  const stageFiles = (files: File[], kind?: AttachmentKind, transferError = "") => {
    if (!allowAttachments || locked || (files.length === 0 && !transferError)) return;
    setAttachMenuOpen(false);
    setAttachError("");
    area.current?.focus();

    // Process gestures in order so every batch shares the remaining slots.
    const jobs = staging.current;
    jobs.pending += 1;
    setStagingAttachments(true);
    jobs.queue = jobs.queue.then(async () => {
      if (staging.current !== jobs) return;
      let error = transferError;
      for (const file of files) {
        if (stagedFiles.current.length >= MAX_ATTACHMENTS) {
          error = `单条消息最多附加 ${MAX_ATTACHMENTS} 个文件`;
          break;
        }
        try {
          const parsed = await readStagedFile(file, kind);
          if (staging.current !== jobs) return;
          updateAttachments([...stagedFiles.current, parsed]);
        } catch (cause) {
          if (staging.current !== jobs) return;
          error = cause instanceof Error ? cause.message : String(cause);
        }
      }
      if (error) setAttachError(error);
    }).finally(() => {
      jobs.pending -= 1;
      if (staging.current === jobs && jobs.pending === 0) setStagingAttachments(false);
    });
  };

  const handleFilesSelected = (kind: AttachmentKind, event: ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(event.currentTarget.files ?? []);
    event.currentTarget.value = "";
    stageFiles(files, kind);
  };

  const closePalette = () => {
    if (paletteStart !== undefined) setDismissed(paletteStart);
    setActiveCandidate(null);
  };

  const hasToolbar = Boolean(allowAttachments || footer || footerRight);

  const attachControl = allowAttachments ? (
    <div className="composer__attach" ref={attachWrapRef}>
      <button
        type="button"
        className="attach-trigger"
        disabled={locked}
        data-active={attachMenuOpen ? "true" : undefined}
        aria-expanded={attachMenuOpen}
        aria-haspopup="menu"
        aria-label="添加附件"
        title="附加图片、PDF 或文本文件"
        onClick={() => setAttachMenuOpen((open) => !open)}
      >
        <svg width="15" height="15" viewBox="0 0 16 16" fill="none" aria-hidden="true">
          <path
            d="M8 3.25v9.5M3.25 8h9.5"
            stroke="currentColor"
            strokeWidth="1.7"
            strokeLinecap="round"
          />
        </svg>
      </button>
      {attachMenuOpen ? (
        <div className="attach-menu" role="menu" aria-label="选择附件类型">
          <button
            type="button"
            role="menuitem"
            className="attach-menu__item"
            onClick={() => triggerPicker("image")}
          >
            <span className="attach-menu__title">图片</span>
            <span className="attach-menu__meta">PNG · JPG · GIF · WebP</span>
          </button>
          <button
            type="button"
            role="menuitem"
            className="attach-menu__item"
            onClick={() => triggerPicker("pdf")}
          >
            <span className="attach-menu__title">PDF 文档</span>
            <span className="attach-menu__meta">.pdf</span>
          </button>
          <button
            type="button"
            role="menuitem"
            className="attach-menu__item"
            onClick={() => triggerPicker("text")}
          >
            <span className="attach-menu__title">文本文件</span>
            <span className="attach-menu__meta">TXT · MD · 代码 · JSON</span>
          </button>
        </div>
      ) : null}

      <input
        ref={imageInputRef}
        type="file"
        accept="image/png,image/jpeg,image/gif,image/webp"
        multiple
        hidden
        onChange={(event) => void handleFilesSelected("image", event)}
      />
      <input
        ref={pdfInputRef}
        type="file"
        accept=".pdf,application/pdf"
        multiple
        hidden
        onChange={(event) => void handleFilesSelected("pdf", event)}
      />
      <input
        ref={textInputRef}
        type="file"
        accept=".txt,.md,.markdown,.json,.jsonl,.csv,.tsv,.log,.yaml,.yml,.toml,.xml,.html,.htm,.css,.scss,.js,.jsx,.ts,.tsx,.go,.py,.rs,.java,.c,.cc,.cpp,.h,.hpp,.sh,.zsh,.sql,.env,.ini,.conf,text/*"
        multiple
        hidden
        onChange={(event) => void handleFilesSelected("text", event)}
      />
    </div>
  ) : null;

  const sendControl =
    disabled && !locked ? (
      <button
        type="button"
        className="send"
        onClick={onStop}
        disabled={stopping || !onStop}
        aria-label={stopping ? "正在停止本轮" : "停止本轮"}
        title={stopping ? "正在停止本轮" : onStop ? "停止本轮（Esc）" : "等待服务提供可停止的轮次"}
      >
        <span className="send__stop-icon" aria-hidden="true" />
      </button>
    ) : (
      <button
        type="button"
        className="send"
        onClick={send}
        disabled={blocked || stagingAttachments || (value.trim().length === 0 && attachments.length === 0)}
        aria-label="发送"
      >
        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
          <path
            d="M8 12.75V3.5M4.25 7L8 3.25L11.75 7"
            stroke="currentColor"
            strokeWidth="1.8"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </svg>
      </button>
    );

  return (
    <div className="composer">
      <div className="composer__inner">
        {showPalette ? (
          <CommandPalette
            id={paletteId}
            items={items}
            kind={paletteKind}
            selectedIndex={selectedIndex}
            emptyLabel={slashItems ? "没有这个命令" : loadingPaths ? "正在读取目录…" : listing ? "没有匹配的路径" : "无法读取目录"}
            onPick={pick}
            onClose={closePalette}
          />
        ) : null}
        {attachError ? <div className="composer__attach-error" role="status">{attachError}</div> : null}
        <div
          className="composer__box"
          data-locked={locked ? "true" : undefined}
          data-toolbar={hasToolbar ? "true" : undefined}
          data-dragging={draggingFiles && !locked ? "true" : undefined}
          aria-busy={stagingAttachments || undefined}
          onPaste={allowAttachments ? (event) => {
            const { files, hasDirectory } = transferredFiles(event.clipboardData);
            if (files.length === 0 && !hasDirectory) return;
            event.preventDefault();
            stageFiles(files, undefined, hasDirectory ? "仅支持粘贴文件" : "");
          } : undefined}
          onDragEnter={allowAttachments ? (event) => {
            if (!hasDraggedFiles(event.dataTransfer)) return;
            event.preventDefault();
            dragDepth.current += 1;
            if (!locked) setDraggingFiles(true);
          } : undefined}
          onDragOver={allowAttachments ? (event) => {
            if (!hasDraggedFiles(event.dataTransfer)) return;
            event.preventDefault();
            event.dataTransfer.dropEffect = locked ? "none" : "copy";
          } : undefined}
          onDragLeave={allowAttachments ? () => {
            dragDepth.current = Math.max(0, dragDepth.current - 1);
            if (dragDepth.current === 0) setDraggingFiles(false);
          } : undefined}
          onDrop={allowAttachments ? (event) => {
            dragDepth.current = 0;
            setDraggingFiles(false);
            const { files, hasDirectory } = transferredFiles(event.dataTransfer);
            if (files.length === 0 && !hasDirectory && !hasDraggedFiles(event.dataTransfer)) return;
            event.preventDefault();
            stageFiles(files, undefined, hasDirectory ? "仅支持拖入文件" : "");
          } : undefined}
        >
          {attachments.length > 0 ? (
            <div className="composer__attachments" aria-label="已附加文件">
              {attachments.map((item) => (
                <div className="attach-chip" key={item.id}>
                  {item.previewUrl ? (
                    <img className="attach-chip__thumb" src={item.previewUrl} alt={item.name} />
                  ) : (
                    <span className="attach-chip__kind">
                      {item.kind === "pdf" ? "PDF" : "文本"}
                    </span>
                  )}
                  <span className="attach-chip__name" title={item.name}>
                    {item.name}
                  </span>
                  <span className="attach-chip__size">{formatBytes(item.size)}</span>
                  <button
                    type="button"
                    className="attach-chip__remove"
                    aria-label={`移除 ${item.name}`}
                    onClick={() => updateAttachments(stagedFiles.current.filter((file) => file.id !== item.id))}
                  >
                    ✕
                  </button>
                </div>
              ))}
            </div>
          ) : null}

          <div className="composer__row">
            <div className="composer__field">
              {command ? (
                <div className="composer__mirror" aria-hidden="true" ref={mirror}>
                  {command.lead}
                  <mark className="composer__command">{command.name}</mark>
                  {command.rest}
                </div>
              ) : null}
              <textarea
                ref={area}
                rows={1}
                value={locked ? "" : value}
                disabled={locked}
                placeholder={placeholder ?? "问点什么…  / 看命令  @ 选文件或子代理"}
                aria-controls={showPalette && paletteKind === "mention" ? paletteId : undefined}
                aria-activedescendant={showPalette && selectedIndex !== null ? `${paletteId}-${selectedIndex}` : undefined}
                autoFocus
                onFocus={(event) => {
                  setFocused(true);
                  syncCaret(event.currentTarget);
                }}
                onBlur={() => {
                  setFocused(false);
                  setActiveCandidate(null);
                }}
                onChange={(event) => {
                  setValue(event.currentTarget.value);
                  syncCaret(event.currentTarget);
                  setDismissed(null);
                  setActiveCandidate(null);
                }}
                onSelect={(event) => syncCaret(event.currentTarget)}
                onScroll={(event) => {
                  const layer = mirror.current;
                  if (layer) layer.scrollTop = event.currentTarget.scrollTop;
                }}
                onCompositionStart={() => {
                  composing.current = true;
                  setCompositionActive(true);
                }}
                onCompositionEnd={(event) => {
                  composing.current = false;
                  justComposed.current = true;
                  setCompositionActive(false);
                  syncCaret(event.currentTarget);
                  window.setTimeout(() => {
                    justComposed.current = false;
                  }, 0);
                }}
                onKeyDown={(event) => {
                  const native = event.nativeEvent;
                  if (composing.current || justComposed.current || native.isComposing || native.keyCode === 229) {
                    if (event.key === "Enter") justComposed.current = false;
                    if (event.key === "Escape") event.stopPropagation();
                    return;
                  }
                  if (event.key === "Escape" && showPalette) {
                    event.preventDefault();
                    closePalette();
                    return;
                  }
                  if (showPalette && paletteKind === "mention" && items.length > 0 &&
                      !event.shiftKey && !event.altKey && !event.metaKey && !event.ctrlKey) {
                    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
                      event.preventDefault();
                      const step = event.key === "ArrowDown" ? 1 : -1;
                      const from = selectedIndex ?? (step === 1 ? -1 : 0);
                      setActiveCandidate({ index: (from + step + items.length) % items.length, request });
                      return;
                    }
                    if (event.key === "Tab" || (event.key === "Enter" && selectedIndex !== null)) {
                      event.preventDefault();
                      pick(selectedIndex ?? 0);
                      return;
                    }
                  }
                  if (event.key !== "Enter" || event.shiftKey) return;
                  event.preventDefault();
                  send();
                }}
              />
            </div>
            {!hasToolbar ? sendControl : null}
          </div>
          {hasToolbar ? (
            <div className="composer__foot">
              {attachControl}
              {footer ? <div className="composer__facts">{footer}</div> : null}
              <div className="composer__right">
                {footerRight}
                {sendControl}
              </div>
            </div>
          ) : null}
        </div>
        {footerOverlay}
      </div>
    </div>
  );
}
