import { useEffect, useMemo, useRef, useState } from "react";
import type { ArtifactDetail, ArtifactExportResult, ArtifactSummary } from "../api";
import {
  artifactKindLabel,
  defaultExportFilename,
  formatArtifactBytes,
  parseCsv,
  sortCsvRows,
  wrapArtifactHtml,
} from "../lib/artifacts";
import { copyText } from "../lib/clipboard";
import type { ResizeHandleProps } from "../lib/useColumnWidth";
import { MarkdownBody } from "./MarkdownBody";

type ViewMode = "preview" | "source";

function formatVersionTime(unixSec: number): string {
  if (!unixSec) return "";
  try {
    return new Date(unixSec * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  } catch {
    return "";
  }
}

export function ArtifactPanel({
  open,
  sessionKey,
  artifacts,
  selectedId,
  selectedVersion,
  detail,
  loading,
  error,
  dark,
  hasWorkspace,
  resizer,
  onSelectArtifact,
  onDeleteArtifact,
  onExportArtifact,
  onRefresh,
  onClose,
}: {
  open: boolean;
  sessionKey: string;
  artifacts: ArtifactSummary[];
  selectedId: string;
  selectedVersion: number;
  detail: ArtifactDetail | null;
  loading: boolean;
  error: string;
  dark: boolean;
  hasWorkspace: boolean;
  resizer: ResizeHandleProps;
  onSelectArtifact: (id: string, version?: number) => void;
  onDeleteArtifact: (id: string) => Promise<void>;
  onExportArtifact: (id: string, version: number, relPath: string) => Promise<ArtifactExportResult>;
  onRefresh: () => void;
  onClose: () => void;
}) {
  const [mode, setMode] = useState<ViewMode>("preview");
  const [wrapLines, setWrapLines] = useState(true);
  const [fullscreen, setFullscreen] = useState(false);
  const [copied, setCopied] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [exportOpen, setExportOpen] = useState(false);
  const [exportPath, setExportPath] = useState("");
  const [exportBusy, setExportBusy] = useState(false);
  const [exportStatus, setExportStatus] = useState<{ kind: "ok" | "error"; text: string } | null>(null);
  const [svgChecker, setSvgChecker] = useState(false);
  const [svgZoom, setSvgZoom] = useState(1);
  const [csvSort, setCsvSort] = useState<{ col: number; dir: "asc" | "desc" } | null>(null);
  const copyTimer = useRef<number | undefined>(undefined);

  useEffect(() => {
    setConfirmDelete(false);
    setExportStatus(null);
    setCsvSort(null);
    setSvgZoom(1);
  }, [selectedId, selectedVersion, sessionKey]);

  useEffect(() => {
    if (!open) {
      setFullscreen(false);
      setConfirmDelete(false);
      setExportOpen(false);
    }
  }, [open]);

  useEffect(() => {
    if (detail) {
      setExportPath(defaultExportFilename(detail.id, detail.kind, detail.language));
    }
  }, [detail?.id, detail?.kind, detail?.language]);

  useEffect(() => () => window.clearTimeout(copyTimer.current), []);

  useEffect(() => {
    if (!fullscreen) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        setFullscreen(false);
      }
    };
    window.addEventListener("keydown", onKeyDown, true);
    return () => window.removeEventListener("keydown", onKeyDown, true);
  }, [fullscreen]);

  const svgUrl = useMemo(() => {
    if (!detail || detail.kind !== "svg") return "";
    const blob = new Blob([detail.content], { type: "image/svg+xml;charset=utf-8" });
    return URL.createObjectURL(blob);
  }, [detail?.id, detail?.kind, detail?.content]);

  useEffect(() => {
    if (!svgUrl) return;
    return () => URL.revokeObjectURL(svgUrl);
  }, [svgUrl]);

  const wrappedHtml = useMemo(() => {
    if (!detail || detail.kind !== "html") return "";
    return wrapArtifactHtml(detail.content, dark);
  }, [detail?.id, detail?.kind, detail?.content, dark]);

  const parsedCsv = useMemo(() => {
    if (!detail || detail.kind !== "csv") return { headers: [], rows: [] };
    return parseCsv(detail.content);
  }, [detail?.id, detail?.kind, detail?.content]);

  const sortedCsvRows = useMemo(() => {
    if (!csvSort) return parsedCsv.rows;
    return sortCsvRows(parsedCsv.rows, csvSort.col, csvSort.dir);
  }, [parsedCsv.rows, csvSort]);

  const codeLines = useMemo(() => {
    if (!detail) return [];
    return detail.content.split(/\r?\n/);
  }, [detail?.content]);

  const supportsRichPreview = Boolean(detail && detail.kind !== "code");
  const effectiveMode: ViewMode = supportsRichPreview ? mode : "source";

  const handleCopy = async () => {
    if (!detail) return;
    if (!(await copyText(detail.content))) return;
    setCopied(true);
    window.clearTimeout(copyTimer.current);
    copyTimer.current = window.setTimeout(() => setCopied(false), 1600);
  };

  const handleDownload = () => {
    if (!detail) return;
    const filename = defaultExportFilename(detail.id, detail.kind, detail.language).split("/").pop() || `${detail.id}.txt`;
    const mime = detail.contentType || "text/plain;charset=utf-8";
    const blob = new Blob([detail.content], { type: mime });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = filename;
    document.body.appendChild(link);
    link.click();
    document.body.removeChild(link);
    window.setTimeout(() => URL.revokeObjectURL(url), 1000);
  };

  const handleDelete = async () => {
    if (!detail) return;
    if (!confirmDelete) {
      setConfirmDelete(true);
      return;
    }
    setDeleting(true);
    try {
      await onDeleteArtifact(detail.id);
      setConfirmDelete(false);
    } catch (cause) {
      setExportStatus({ kind: "error", text: cause instanceof Error ? cause.message : String(cause) });
    } finally {
      setDeleting(false);
    }
  };

  const handleExportSubmit = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!detail || exportBusy) return;
    setExportBusy(true);
    setExportStatus(null);
    try {
      const res = await onExportArtifact(detail.id, detail.activeVersion, exportPath.trim());
      setExportStatus({ kind: "ok", text: `已保存到工作区：${res.path} (${formatArtifactBytes(res.bytes)})` });
    } catch (cause) {
      setExportStatus({ kind: "error", text: cause instanceof Error ? cause.message : String(cause) });
    } finally {
      setExportBusy(false);
    }
  };

  const toggleCsvSort = (col: number) => {
    setCsvSort((prev) => {
      if (!prev || prev.col !== col) return { col, dir: "asc" };
      if (prev.dir === "asc") return { col, dir: "desc" };
      return null;
    });
  };

  const currentSummary = artifacts.find((item) => item.id === selectedId) ?? detail;
  const activeVersionMeta = detail?.versions.find((v) => v.version === detail.activeVersion);

  return (
    <aside
      className={fullscreen ? "drawer artifact-drawer artifact-drawer--fullscreen" : "drawer artifact-drawer"}
      aria-label="产物预览"
      aria-hidden={!open || undefined}
      aria-modal={fullscreen || undefined}
    >
      {fullscreen ? null : (
        <div className="resizer resizer--left" title="拖动改变产物面板宽度，双击复位" {...resizer} />
      )}
      <div className="drawer__surface">
        <div className="drawer__head">
          <span className="drawer__title" title={currentSummary ? currentSummary.title || currentSummary.id : "产物"}>
            {currentSummary ? currentSummary.title || currentSummary.id : "产物"}
          </span>
          {currentSummary ? (
            <span className="chip">
              {artifactKindLabel(currentSummary.kind, currentSummary.language)}
              {" · "}
              {formatArtifactBytes(detail ? detail.bytes : currentSummary.bytes)}
            </span>
          ) : null}
          <button
            type="button"
            className="icon-button"
            onClick={onRefresh}
            title="刷新产物列表"
            aria-label="刷新产物列表"
          >
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              <path d="M21 12a9 9 0 1 1-2.64-6.36" />
              <path d="M21 3v6h-6" />
            </svg>
          </button>
          <button
            type="button"
            className="icon-button"
            onClick={() => setFullscreen((value) => !value)}
            title={fullscreen ? "退出全屏" : "全屏预览"}
            aria-label={fullscreen ? "退出全屏" : "全屏预览"}
          >
            {fullscreen ? (
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                <path d="M8 3v3a2 2 0 0 1-2 2H3" />
                <path d="M21 8h-3a2 2 0 0 1-2-2V3" />
                <path d="M3 16h3a2 2 0 0 1 2 2v3" />
                <path d="M16 21v-3a2 2 0 0 1 2-2h3" />
              </svg>
            ) : (
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                <path d="M15 3h6v6" />
                <path d="M9 21H3v-6" />
                <path d="M21 3l-7 7" />
                <path d="M3 21l7-7" />
              </svg>
            )}
          </button>
          <button
            type="button"
            className="icon-button"
            onClick={() => {
              setFullscreen(false);
              onClose();
            }}
            aria-label="关闭"
            title="关闭"
          >
            <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
              <path d="M3.5 3.5L10.5 10.5M10.5 3.5L3.5 10.5" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
            </svg>
          </button>
        </div>

        {artifacts.length > 0 ? (
          <div className="artifact-bar">
            {artifacts.length > 1 ? (
              <div className="artifact-tabs" role="tablist" aria-label="会话产物列表">
                {artifacts.map((item) => {
                  const active = item.id === selectedId;
                  return (
                    <button
                      key={item.id}
                      type="button"
                      role="tab"
                      aria-selected={active}
                      className="artifact-tab"
                      data-active={active ? "true" : undefined}
                      onClick={() => onSelectArtifact(item.id, 0)}
                      title={`${item.title || item.id} (v${item.version})`}
                    >
                      <span className="artifact-tab__name">{item.title || item.id}</span>
                      <span className="artifact-tab__ver">v{item.version}</span>
                    </button>
                  );
                })}
              </div>
            ) : null}

            {detail ? (
              <div className="artifact-controls">
                <div className="artifact-controls__left">
                  {supportsRichPreview ? (
                    <div className="artifact-segment" role="group" aria-label="显示模式">
                      <button
                        type="button"
                        data-active={effectiveMode === "preview" ? "true" : undefined}
                        onClick={() => setMode("preview")}
                      >
                        预览
                      </button>
                      <button
                        type="button"
                        data-active={effectiveMode === "source" ? "true" : undefined}
                        onClick={() => setMode("source")}
                      >
                        源码
                      </button>
                    </div>
                  ) : null}

                  {detail.versions.length > 1 ? (
                    <select
                      className="artifact-select"
                      aria-label="选择产物版本"
                      value={detail.activeVersion}
                      onChange={(e) => onSelectArtifact(detail.id, Number(e.target.value))}
                    >
                      {detail.versions
                        .slice()
                        .sort((a, b) => b.version - a.version)
                        .map((ver) => (
                          <option key={ver.version} value={ver.version}>
                            {`v${ver.version}${ver.version === detail.version ? " (最新)" : ""}${formatVersionTime(ver.createdAt) ? ` · ${formatVersionTime(ver.createdAt)}` : ""}`}
                          </option>
                        ))}
                    </select>
                  ) : (
                    <span className="artifact-ver-pill">v{detail.activeVersion}</span>
                  )}

                  {effectiveMode === "source" ? (
                    <button
                      type="button"
                      className={wrapLines ? "chip chip--button chip--accent" : "chip chip--button"}
                      onClick={() => setWrapLines((v) => !v)}
                    >
                      自动换行
                    </button>
                  ) : null}
                </div>

                <div className="artifact-controls__right">
                  <button
                    type="button"
                    className="chip chip--button"
                    onClick={() => void handleCopy()}
                    title="复制产物源码"
                  >
                    {copied ? "已复制" : "复制"}
                  </button>
                  <button
                    type="button"
                    className="chip chip--button"
                    onClick={handleDownload}
                    title="下载到本地"
                  >
                    下载
                  </button>
                  {hasWorkspace ? (
                    <button
                      type="button"
                      className={exportOpen ? "chip chip--button chip--accent" : "chip chip--button"}
                      onClick={() => {
                        setExportStatus(null);
                        setExportOpen((v) => !v);
                      }}
                      title="写入当前工作区目录"
                    >
                      导出工作区
                    </button>
                  ) : null}
                  <button
                    type="button"
                    className={confirmDelete ? "chip chip--button artifact-delete--confirm" : "chip chip--button"}
                    disabled={deleting}
                    onClick={() => void handleDelete()}
                    title={confirmDelete ? "再次点击确认删除全部版本" : "删除产物"}
                  >
                    {deleting ? "删除中…" : confirmDelete ? "确认删除" : "删除"}
                  </button>
                </div>
              </div>
            ) : null}

            {activeVersionMeta?.summary ? (
              <div className="artifact-note" title={activeVersionMeta.summary}>
                {activeVersionMeta.action === "patch" ? "增量更新：" : "版本说明："}
                {activeVersionMeta.summary}
              </div>
            ) : null}

            {exportOpen && detail ? (
              <form className="artifact-export" onSubmit={(e) => void handleExportSubmit(e)}>
                <input
                  type="text"
                  className="artifact-export__input"
                  value={exportPath}
                  onChange={(e) => setExportPath(e.target.value)}
                  placeholder="工作区相对路径，如 artifacts/chart.html"
                  aria-label="工作区导出路径"
                />
                <button type="submit" className="chip chip--button chip--accent" disabled={exportBusy}>
                  {exportBusy ? "写入中…" : "确认写入"}
                </button>
              </form>
            ) : null}

            {exportStatus ? (
              <div className={exportStatus.kind === "error" ? "notice notice--error artifact-status" : "artifact-status artifact-status--ok"}>
                {exportStatus.text}
              </div>
            ) : null}
          </div>
        ) : null}

        <div className="artifact-stage">
          {error ? <div className="notice notice--error">{error}</div> : null}
          {loading && !detail ? <div className="rail__empty">加载产物中…</div> : null}

          {!loading && !error && artifacts.length === 0 ? (
            <div className="artifact-empty">
              <p className="artifact-empty__title">当前会话还没有生成产物</p>
              <p className="artifact-empty__hint">
                让 Atlas 制作网页看板、SVG 流程图、Markdown 报告或 CSV 表格时，产物卡片和预览会自动出现在这里。
              </p>
            </div>
          ) : null}

          {detail && effectiveMode === "preview" && detail.kind === "html" ? (
            <iframe
              key={`${detail.id}:${detail.activeVersion}:${detail.sha256}`}
              className="artifact-iframe"
              sandbox="allow-scripts"
              srcDoc={wrappedHtml}
              title={detail.title || detail.id}
            />
          ) : null}

          {detail && effectiveMode === "preview" && detail.kind === "svg" ? (
            <div className="artifact-svg-wrap" data-checker={svgChecker ? "true" : undefined}>
              <div className="artifact-svg-toolbar">
                <button
                  type="button"
                  className={svgChecker ? "chip chip--button chip--accent" : "chip chip--button"}
                  onClick={() => setSvgChecker((v) => !v)}
                >
                  棋盘背景
                </button>
                <div className="artifact-svg-zoom">
                  <button type="button" className="chip chip--button" onClick={() => setSvgZoom((z) => Math.max(0.25, Number((z - 0.25).toFixed(2))))}>
                    −
                  </button>
                  <button type="button" className="chip chip--button" onClick={() => setSvgZoom(1)}>
                    {Math.round(svgZoom * 100)}%
                  </button>
                  <button type="button" className="chip chip--button" onClick={() => setSvgZoom((z) => Math.min(4, Number((z + 0.25).toFixed(2))))}>
                    +
                  </button>
                </div>
              </div>
              <div className="artifact-svg-canvas">
                {svgUrl ? (
                  <img
                    className="artifact-svg-img"
                    src={svgUrl}
                    alt={detail.title || detail.id}
                    style={{ transform: `scale(${svgZoom})` }}
                  />
                ) : null}
              </div>
            </div>
          ) : null}

          {detail && effectiveMode === "preview" && detail.kind === "markdown" ? (
            <div className="artifact-markdown">
              <MarkdownBody text={detail.content} />
            </div>
          ) : null}

          {detail && effectiveMode === "preview" && detail.kind === "csv" ? (
            <div className="artifact-csv-wrap">
              <div className="artifact-csv-meta">
                {parsedCsv.rows.length} 行 × {parsedCsv.headers.length} 列
              </div>
              <div className="artifact-csv-scroll">
                <table className="artifact-csv-table">
                  <thead>
                    <tr>
                      <th className="artifact-csv-idx">#</th>
                      {parsedCsv.headers.map((header, idx) => {
                        const activeSort = csvSort?.col === idx ? csvSort.dir : null;
                        return (
                          <th key={idx}>
                            <button
                              type="button"
                              className="artifact-csv-sort"
                              onClick={() => toggleCsvSort(idx)}
                            >
                              <span>{header || `列 ${idx + 1}`}</span>
                              <span className="artifact-csv-arrow">
                                {activeSort === "asc" ? "▲" : activeSort === "desc" ? "▼" : "↕"}
                              </span>
                            </button>
                          </th>
                        );
                      })}
                    </tr>
                  </thead>
                  <tbody>
                    {sortedCsvRows.map((row, rIdx) => (
                      <tr key={rIdx}>
                        <td className="artifact-csv-idx">{rIdx + 1}</td>
                        {row.map((cell, cIdx) => (
                          <td key={cIdx}>{cell}</td>
                        ))}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          ) : null}

          {detail && effectiveMode === "source" ? (
            <div className="artifact-code-wrap" data-wrap={wrapLines ? "true" : "false"}>
              <div className="artifact-code">
                {codeLines.map((line, idx) => (
                  <div className="artifact-code__row" key={idx}>
                    <span className="artifact-code__ln">{idx + 1}</span>
                    <span className="artifact-code__txt">{line || " "}</span>
                  </div>
                ))}
              </div>
            </div>
          ) : null}
        </div>

        <div className="drawer__foot">
          <span title={detail?.sha256 ? `sha256: ${detail.sha256}` : undefined}>
            {detail ? `${detail.id} · v${detail.activeVersion}/${detail.version}` : `${artifacts.length} 个产物`}
          </span>
          <span>ESC 可退出全屏</span>
        </div>
      </div>
    </aside>
  );
}
