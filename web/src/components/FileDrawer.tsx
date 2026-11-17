import { useCallback, useEffect, useState } from "react";
import { api, type DirListing, type FileContent } from "../api";

// The file drawer.
//
// It browses one directory at a time rather than holding a whole tree: a folder
// is only read when it is opened, so a large repository costs nothing until the
// reader asks for that part of it.
export function FileDrawer({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [listing, setListing] = useState<DirListing | null>(null);
  const [file, setFile] = useState<FileContent | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const browse = useCallback(async (path: string) => {
    setBusy(true);
    setError("");
    try {
      setListing(await api.files(path));
      setFile(null);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  }, []);

  useEffect(() => {
    if (!open) return;
    void browse("");
  }, [open, browse]);

  if (!open) return null;

  const openFile = async (path: string) => {
    setBusy(true);
    setError("");
    try {
      setFile(await api.file(path));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  };

  const parent = () => {
    if (!listing) return "";
    const parts = listing.path.split("/").filter(Boolean);
    parts.pop();
    return parts.join("/");
  };

  return (
    <aside className="drawer">
      <div className="drawer__head">
        <span className="drawer__title">{file ? file.path : listing?.path || "工作目录"}</span>
        {file ? (
          <button type="button" className="icon-button" onClick={() => setFile(null)} title="返回">
            ←
          </button>
        ) : listing?.path ? (
          <button type="button" className="icon-button" onClick={() => void browse(parent())} title="上一级">
            ↑
          </button>
        ) : null}
        <button type="button" className="icon-button" onClick={onClose} aria-label="关闭">
          ✕
        </button>
      </div>

      <div className="drawer__scroll">
        {error ? <div className="notice notice--error">{error}</div> : null}
        {busy ? <div className="rail__empty">读取中…</div> : null}

        {file ? (
          <div className="file-view">
            <div className="file-view__bar">
              <span>{file.binary ? "二进制文件" : `${file.size} 字节`}</span>
              {file.truncated ? <span>已截断</span> : null}
            </div>
            {file.binary ? (
              <div className="rail__empty">该文件不是文本，未显示内容。</div>
            ) : (
              <pre className="file-view__code">{file.text}</pre>
            )}
          </div>
        ) : null}

        {!file && listing
          ? listing.entries.map((entry) => (
              <button
                key={entry.path}
                type="button"
                className="file-row"
                onClick={() => (entry.isDir ? void browse(entry.path) : void openFile(entry.path))}
              >
                <span className="file-row__glyph">{entry.isDir ? "▸" : "·"}</span>
                <span className="file-row__name">{entry.name}</span>
                {entry.isDir ? null : <span className="file-row__size">{size(entry.size ?? 0)}</span>}
              </button>
            ))
          : null}

        {!file && listing && listing.entries.length === 0 ? (
          <div className="rail__empty">空目录。</div>
        ) : null}
      </div>

      <div className="drawer__foot">只读 · 点目录进入，点文件预览</div>
    </aside>
  );
}

function size(bytes: number): string {
  if (bytes < 1024) return `${bytes}B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)}K`;
  return `${(bytes / 1024 / 1024).toFixed(1)}M`;
}
