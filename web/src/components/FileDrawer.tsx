import { useCallback, useEffect, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { api, type DirListing, type FileContent, type FileEntry } from "../api";
import type { ResizeHandleProps } from "../lib/useColumnWidth";

const MAX_TREE_DEPTH = 32;

// Lazy file tree drawer rooted at the active session's working directory.
// Each folder is fetched only when expanded, and all cached nodes reset
// whenever the active session or its directory changes.
export function FileDrawer({
  open,
  sessionKey,
  rootDir,
  running,
  resizer,
  onClose,
}: {
  open: boolean;
  sessionKey: string;
  rootDir?: string;
  running: boolean;
  resizer: ResizeHandleProps;
  onClose: () => void;
}) {
  const [effectiveRoot, setEffectiveRoot] = useState(rootDir ?? "");
  const [childrenByDir, setChildrenByDir] = useState<Record<string, FileEntry[]>>({});
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const [loadingDirs, setLoadingDirs] = useState<Record<string, boolean>>({});
  const [file, setFile] = useState<FileContent | null>(null);
  const [selectedPath, setSelectedPath] = useState("");
  const [fileBusy, setFileBusy] = useState(false);
  const [error, setError] = useState("");

  const epochRef = useRef(0);
  const expandedRef = useRef<Record<string, boolean>>({});
  const fileRef = useRef<FileContent | null>(null);
  const prevRunningRef = useRef(running);

  const setExpandedState = useCallback((updater: Record<string, boolean> | ((prev: Record<string, boolean>) => Record<string, boolean>)) => {
    setExpanded((prev) => {
      const next = typeof updater === "function" ? updater(prev) : updater;
      expandedRef.current = next;
      return next;
    });
  }, []);

  const setFileState = useCallback((next: FileContent | null) => {
    fileRef.current = next;
    setFile(next);
  }, []);

  const loadRootAndReset = useCallback(async () => {
    const epoch = ++epochRef.current;
    expandedRef.current = {};
    fileRef.current = null;
    setExpanded({});
    setChildrenByDir({});
    setFile(null);
    setSelectedPath("");
    setFileBusy(false);
    setError("");
    setEffectiveRoot(rootDir ?? "");

    if (!open) {
      setLoadingDirs({});
      return;
    }

    setLoadingDirs({ "": true });
    try {
      const listing: DirListing = await api.files("");
      if (epoch !== epochRef.current) return;
      setEffectiveRoot(listing.root || rootDir || "");
      setChildrenByDir({ "": listing.entries });
    } catch (cause) {
      if (epoch !== epochRef.current) return;
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      if (epoch === epochRef.current) {
        setLoadingDirs({});
      }
    }
  }, [open, rootDir]);

  useEffect(() => {
    void loadRootAndReset();
  }, [open, sessionKey, rootDir, loadRootAndReset]);

  const refresh = useCallback(async () => {
    if (!open) return;
    const epoch = ++epochRef.current;
    const dirsToRefresh = ["", ...Object.keys(expandedRef.current).filter((dir) => expandedRef.current[dir])];
    const activeFilePath = fileRef.current?.path;

    setError("");
    setLoadingDirs({ "": true });

    const dirResults = await Promise.allSettled(
      dirsToRefresh.map(async (dir) => ({
        dir,
        listing: await api.files(dir),
      })),
    );
    if (epoch !== epochRef.current) return;

    const nextChildren: Record<string, FileEntry[]> = {};
    const stillExpanded: Record<string, boolean> = {};
    let rootError = "";

    for (let i = 0; i < dirResults.length; i += 1) {
      const dir = dirsToRefresh[i];
      const result = dirResults[i];
      if (result.status === "fulfilled") {
        nextChildren[dir] = result.value.listing.entries;
        if (dir === "") {
          setEffectiveRoot(result.value.listing.root || rootDir || "");
        } else {
          stillExpanded[dir] = true;
        }
      } else if (dir === "") {
        rootError = result.reason instanceof Error ? result.reason.message : String(result.reason);
      }
    }

    if (rootError) {
      setError(rootError);
    } else {
      setChildrenByDir(nextChildren);
      setExpandedState(stillExpanded);
    }
    setLoadingDirs({});

    if (activeFilePath) {
      try {
        const refreshedFile = await api.file(activeFilePath);
        if (epoch !== epochRef.current) return;
        setFileState(refreshedFile);
      } catch {
        if (epoch !== epochRef.current) return;
        setFileState(null);
      }
    }
  }, [open, rootDir, setExpandedState, setFileState]);

  useEffect(() => {
    const wasRunning = prevRunningRef.current;
    prevRunningRef.current = running;
    if (!open || !wasRunning || running) return;
    void refresh();
  }, [open, running, refresh]);

  const loadDir = async (dirPath: string) => {
    const epoch = epochRef.current;
    setLoadingDirs((prev) => ({ ...prev, [dirPath]: true }));
    setError("");
    try {
      const listing = await api.files(dirPath);
      if (epoch !== epochRef.current) return;
      if (listing.root) {
        setEffectiveRoot(listing.root);
      }
      setChildrenByDir((prev) => ({ ...prev, [dirPath]: listing.entries }));
    } catch (cause) {
      if (epoch !== epochRef.current) return;
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      if (epoch === epochRef.current) {
        setLoadingDirs((prev) => {
          const next = { ...prev };
          delete next[dirPath];
          return next;
        });
      }
    }
  };

  const toggleDir = (dirPath: string) => {
    setSelectedPath(dirPath);
    const currentlyExpanded = Boolean(expandedRef.current[dirPath]);
    if (currentlyExpanded) {
      setExpandedState((prev) => ({ ...prev, [dirPath]: false }));
      return;
    }
    setExpandedState((prev) => ({ ...prev, [dirPath]: true }));
    if (!(dirPath in childrenByDir) && !loadingDirs[dirPath]) {
      void loadDir(dirPath);
    }
  };

  const openFile = async (path: string) => {
    const epoch = epochRef.current;
    setSelectedPath(path);
    setFileBusy(true);
    setError("");
    try {
      const content = await api.file(path);
      if (epoch !== epochRef.current) return;
      setFileState(content);
    } catch (cause) {
      if (epoch !== epochRef.current) return;
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      if (epoch === epochRef.current) {
        setFileBusy(false);
      }
    }
  };

  const rootTitle = basename(effectiveRoot) || "工作目录";
  const headerTitle = file ? basename(file.path) || file.path : rootTitle;
  const fullPath = file
    ? joinDisplayPath(effectiveRoot, file.path)
    : effectiveRoot || "当前会话工作目录";

  const rootEntries = childrenByDir[""];
  const rootLoading = Boolean(loadingDirs[""]);

  const renderTree = (dirPath: string, depth: number): ReactNode => {
    if (depth >= MAX_TREE_DEPTH) return null;
    const entries = childrenByDir[dirPath];
    if (!entries) return null;

    return entries.map((entry) => {
      const isOpen = entry.isDir && Boolean(expanded[entry.path]);
      const isSelected = selectedPath === entry.path;
      const isDirLoading = entry.isDir && Boolean(loadingDirs[entry.path]);
      const hasChildren = entry.isDir && entry.path in childrenByDir;
      const childEntries = entry.isDir ? childrenByDir[entry.path] : undefined;
      const depthStyle = { "--depth": depth } as CSSProperties;

      return (
        <div key={entry.path}>
          <button
            type="button"
            className="file-row"
            data-selected={isSelected ? "true" : undefined}
            style={depthStyle}
            title={entry.path}
            onClick={() => (entry.isDir ? toggleDir(entry.path) : void openFile(entry.path))}
          >
            <span className="file-row__glyph">{entry.isDir ? (isOpen ? "▾" : "▸") : "·"}</span>
            <span className="file-row__name">{entry.name}</span>
            {entry.isDir ? null : <span className="file-row__size">{size(entry.size ?? 0)}</span>}
          </button>

          {entry.isDir && isOpen ? (
            isDirLoading && !hasChildren ? (
              <div className="file-row__hint" style={depthStyle}>
                读取中…
              </div>
            ) : hasChildren && (childEntries?.length ?? 0) === 0 ? (
              <div className="file-row__hint" style={depthStyle}>
                空目录
              </div>
            ) : (
              renderTree(entry.path, depth + 1)
            )
          ) : null}
        </div>
      );
    });
  };

  return (
    <aside
      className="drawer"
      data-open={open ? "true" : "false"}
      aria-label="文件树"
      aria-hidden={!open || undefined}
    >
      <div className="resizer resizer--left" title="拖动改变文件树宽度，双击复位" {...resizer} />

      <div className="drawer__surface">
        <div className="drawer__head">
          <span className="drawer__title" title={file ? file.path : effectiveRoot || "工作目录"}>
            {headerTitle}
          </span>
          {file ? (
            <button type="button" className="icon-button" onClick={() => setFileState(null)} title="返回文件树" aria-label="返回文件树">
              ←
            </button>
          ) : null}
          <button type="button" className="icon-button" onClick={() => void refresh()} title="刷新文件树" aria-label="刷新文件树">
            ↻
          </button>
          <button type="button" className="icon-button" onClick={onClose} aria-label="关闭">
            ✕
          </button>
        </div>

        <div className="drawer__path" title={fullPath}>
          {fullPath}
        </div>

        <div className="drawer__scroll">
          {error ? <div className="notice notice--error">{error}</div> : null}
          {fileBusy || (rootLoading && !rootEntries && !file) ? <div className="rail__empty">读取中…</div> : null}

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

          {!file && rootEntries ? renderTree("", 0) : null}

          {!file && rootEntries && rootEntries.length === 0 && !rootLoading ? (
            <div className="rail__empty">空目录。</div>
          ) : null}
        </div>

        <div className="drawer__foot">
          {file ? "只读预览 · ← 返回文件树" : "只读 · 点目录展开，点文件预览，左边线可拖宽"}
        </div>
      </div>
    </aside>
  );
}

function basename(path: string): string {
  const clean = path.replace(/[\\/]+$/, "");
  if (!clean) return "";
  const idx = Math.max(clean.lastIndexOf("/"), clean.lastIndexOf("\\"));
  return idx >= 0 ? clean.slice(idx + 1) || clean : clean;
}

function joinDisplayPath(root: string, rel: string): string {
  if (!root) return rel;
  if (!rel) return root;
  const sep = root.includes("\\") && !root.includes("/") ? "\\" : "/";
  const base = root.replace(/[\\/]+$/, "");
  const cleanRel = rel.replace(/^[\\/]+/, "");
  return `${base}${sep}${cleanRel}`;
}

function size(bytes: number): string {
  if (bytes < 1024) return `${bytes}B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)}K`;
  return `${(bytes / 1024 / 1024).toFixed(1)}M`;
}
