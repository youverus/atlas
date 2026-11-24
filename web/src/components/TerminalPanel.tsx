import { useCallback, useEffect, useRef, useState } from "react";
import { Terminal as XTerm } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { api, ApiError } from "../api";
import { readAtlasTerminalTheme } from "../lib/terminalTheme";
import type { VerticalResizeHandleProps } from "../lib/usePanelHeight";

interface TerminalTab {
  id: string;
  dirKey: string;
  index: number;
  shellLabel: string;
  cwd: string;
}

interface WorkspaceTerminals {
  tabs: TerminalTab[];
  activeId: string;
}

const DEFAULT_FONT_FAMILY =
  "Geist Mono, ui-monospace, 'SF Mono', SFMono-Regular, Menlo, Monaco, Consolas, monospace";

function decodeBase64Bytes(raw: string): Uint8Array {
  const binary = window.atob(raw);
  const out = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) {
    out[i] = binary.charCodeAt(i);
  }
  return out;
}

function encodeBinaryToBase64(raw: string): string {
  let bytes = "";
  for (let i = 0; i < raw.length; i += 1) {
    bytes += String.fromCharCode(raw.charCodeAt(i) & 0xff);
  }
  return window.btoa(bytes);
}

function shellDisplayName(shellPath: string | undefined): string {
  if (!shellPath) return "shell";
  const parts = shellPath.split(/[\\/]/);
  const last = parts[parts.length - 1] || shellPath;
  return last.replace(/\.exe$/i, "") || "shell";
}

function dirLeafName(dir: string): string {
  const cleaned = dir.replace(/[\\/]+$/, "");
  if (!cleaned) return "/";
  const idx = Math.max(cleaned.lastIndexOf("/"), cleaned.lastIndexOf("\\"));
  return idx >= 0 ? cleaned.slice(idx + 1) || "/" : cleaned;
}

function nextTabIndex(tabs: TerminalTab[]): number {
  const used = new Set(tabs.map((t) => t.index));
  for (let i = 1; ; i += 1) {
    if (!used.has(i)) return i;
  }
}

/**
 * Single interactive xterm.js + PTY instance. Keeps its PTY alive across
 * panel collapse/expand and only disposes when its tab is explicitly closed.
 */
function TerminalSessionView({
  tab,
  visible,
  resizing,
  onMetaChange,
  onError,
  onExited,
}: {
  tab: TerminalTab;
  visible: boolean;
  resizing: boolean;
  onMetaChange: (tabId: string, shellLabel: string, cwd: string) => void;
  onError: (tabId: string, message: string) => void;
  onExited: (tabId: string) => void;
}) {
  const hostRef = useRef<HTMLDivElement>(null);
  const termRef = useRef<XTerm | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const ptyIdRef = useRef<string | null>(null);
  const visibleRef = useRef(visible);
  const resizingRef = useRef(resizing);
  const lastSizeRef = useRef<{ cols: number; rows: number } | null>(null);
  const metaCallbackRef = useRef(onMetaChange);
  const errorCallbackRef = useRef(onError);
  const exitCallbackRef = useRef(onExited);

  visibleRef.current = visible;
  resizingRef.current = resizing;
  metaCallbackRef.current = onMetaChange;
  errorCallbackRef.current = onError;
  exitCallbackRef.current = onExited;

  const syncSize = useCallback(() => {
    const term = termRef.current;
    const fit = fitRef.current;
    const el = hostRef.current;
    if (!term || !fit || !el || !visibleRef.current) return;
    if (el.clientWidth < 16 || el.clientHeight < 16) return;

    fit.fit();
    const cols = term.cols;
    const rows = term.rows;
    const ptyId = ptyIdRef.current;
    if (!ptyId || cols < 1 || rows < 1) return;
    const prev = lastSizeRef.current;
    if (prev && prev.cols === cols && prev.rows === rows) return;
    lastSizeRef.current = { cols, rows };
    void api.resizeTerminal(ptyId, cols, rows).catch(() => undefined);
  }, []);

  useEffect(() => {
    const container = hostRef.current;
    if (!container) return;

    let disposed = false;
    let stream: EventSource | null = null;
    let writeInFlight = false;
    let queuedText = "";

    const term = new XTerm({
      fontFamily: DEFAULT_FONT_FAMILY,
      fontSize: 12,
      lineHeight: 1.35,
      cursorBlink: true,
      scrollback: 5000,
      theme: readAtlasTerminalTheme(),
      allowProposedApi: false,
    });
    const fitAddon = new FitAddon();
    term.loadAddon(fitAddon);
    term.open(container);
    termRef.current = term;
    fitRef.current = fitAddon;

    if (visibleRef.current && container.clientWidth >= 16 && container.clientHeight >= 16) {
      fitAddon.fit();
      term.focus();
    }

    const flushWrites = async () => {
      if (writeInFlight) return;
      writeInFlight = true;
      try {
        while (!disposed && queuedText.length > 0 && ptyIdRef.current) {
          const chunk = queuedText;
          queuedText = "";
          await api.writeTerminal(ptyIdRef.current, chunk);
        }
      } catch {
        // Ignore transient write failures after process exit.
      } finally {
        writeInFlight = false;
        if (!disposed && queuedText.length > 0 && ptyIdRef.current) {
          void flushWrites();
        }
      }
    };

    const dataSub = term.onData((data) => {
      if (disposed) return;
      queuedText += data;
      void flushWrites();
    });

    const binarySub = term.onBinary((data) => {
      const id = ptyIdRef.current;
      if (disposed || !id) return;
      void api.writeTerminal(id, encodeBinaryToBase64(data), "base64").catch(() => undefined);
    });

    const themeObserver = new MutationObserver(() => {
      term.options.theme = readAtlasTerminalTheme();
    });
    themeObserver.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["class"],
    });

    let resizeTimer: number | null = null;
    let resizeRaf = 0;
    const resizeObserver = new ResizeObserver(() => {
      if (disposed || !visibleRef.current) return;
      if (resizingRef.current) {
        if (resizeTimer === null) {
          resizeTimer = window.setTimeout(() => {
            resizeTimer = null;
            syncSize();
          }, 120);
        }
        return;
      }
      if (resizeRaf) {
        window.cancelAnimationFrame(resizeRaf);
      }
      resizeRaf = window.requestAnimationFrame(() => {
        resizeRaf = 0;
        syncSize();
      });
    });
    resizeObserver.observe(container);

    const initCols = term.cols > 0 ? term.cols : 80;
    const initRows = term.rows > 0 ? term.rows : 24;
    lastSizeRef.current = { cols: initCols, rows: initRows };

    api
      .createTerminal(initCols, initRows)
      .then((info) => {
        if (disposed) {
          void api.closeTerminal(info.id).catch(() => undefined);
          return;
        }
        ptyIdRef.current = info.id;
        metaCallbackRef.current(
          tab.id,
          shellDisplayName(info.shell),
          info.cwd || tab.cwd,
        );

        const es = new EventSource(`/api/terminal/stream?id=${encodeURIComponent(info.id)}`);
        stream = es;
        es.onmessage = (event) => {
          if (disposed) return;
          try {
            const payload = JSON.parse(event.data) as {
              type: string;
              data?: string;
              exitCode?: number;
            };
            if (payload.type === "data" && payload.data) {
              term.write(decodeBase64Bytes(payload.data));
            } else if (payload.type === "exit") {
              es.close();
              exitCallbackRef.current(tab.id);
            }
          } catch {
            // Ignore malformed stream lines.
          }
        };

        if (visibleRef.current) {
          window.requestAnimationFrame(() => {
            if (!disposed) {
              syncSize();
              term.focus();
            }
          });
        }
        if (queuedText.length > 0) {
          void flushWrites();
        }
      })
      .catch((cause) => {
        if (disposed) return;
        const message =
          cause instanceof ApiError && cause.status === 501
            ? "当前运行环境不支持内置终端"
            : cause instanceof Error
              ? cause.message
              : String(cause);
        errorCallbackRef.current(tab.id, message);
      });

    return () => {
      disposed = true;
      if (resizeTimer !== null) window.clearTimeout(resizeTimer);
      if (resizeRaf) window.cancelAnimationFrame(resizeRaf);
      resizeObserver.disconnect();
      themeObserver.disconnect();
      dataSub.dispose();
      binarySub.dispose();
      if (stream) {
        stream.close();
      }
      const ptyId = ptyIdRef.current;
      ptyIdRef.current = null;
      if (ptyId) {
        void api.closeTerminal(ptyId).catch(() => undefined);
      }
      term.dispose();
      termRef.current = null;
      fitRef.current = null;
    };
  }, [tab.id, tab.cwd, syncSize]);

  useEffect(() => {
    if (!visible) return;
    const frame = window.requestAnimationFrame(() => {
      syncSize();
      termRef.current?.focus();
    });
    return () => window.cancelAnimationFrame(frame);
  }, [visible, resizing, syncSize]);

  return <div ref={hostRef} className="terminal-panel__xterm" />;
}

/**
 * Embedded bottom terminal panel matching zcode's WorkspaceTerminalToggleButton
 * + AnimatedTerminalPanel behavior: lazy-mounted on first open, kept alive on
 * collapse, scoped per working directory, and resizable from its top border.
 */
export function TerminalPanel({
  open,
  directory,
  deviceId = "",
  height,
  resizing,
  resizer,
  onClose,
}: {
  open: boolean;
  directory: string;
  deviceId?: string;
  height: number;
  resizing: boolean;
  resizer: VerticalResizeHandleProps;
  onClose: () => void;
}) {
  const dirKey = deviceId ? `${deviceId}:${directory || "__default__"}` : directory || "__default__";
  const [hasOpened, setHasOpened] = useState(open);
  const [workspaces, setWorkspaces] = useState<Record<string, WorkspaceTerminals>>({});
  const [errorsByTab, setErrorsByTab] = useState<Record<string, string>>({});
  const seqRef = useRef(0);

  const createTabObject = useCallback(
    (targetDirKey: string, cwd: string, index: number): TerminalTab => {
      seqRef.current += 1;
      return {
        id: `t-${seqRef.current}`,
        dirKey: targetDirKey,
        index,
        shellLabel: "shell",
        cwd,
      };
    },
    [],
  );

  useEffect(() => {
    if (!open) return;
    setHasOpened(true);
    setWorkspaces((prev) => {
      const existing = prev[dirKey];
      if (existing && existing.tabs.length > 0) {
        return prev;
      }
      const tab = createTabObject(dirKey, directory, 1);
      return {
        ...prev,
        [dirKey]: { tabs: [tab], activeId: tab.id },
      };
    });
  }, [open, dirKey, directory, createTabObject]);

  const handleMetaChange = useCallback((tabId: string, shellLabel: string, cwd: string) => {
    setWorkspaces((prev) => {
      let changed = false;
      const next: Record<string, WorkspaceTerminals> = {};
      for (const [key, ws] of Object.entries(prev)) {
        const idx = ws.tabs.findIndex((t) => t.id === tabId);
        if (idx === -1) {
          next[key] = ws;
          continue;
        }
        changed = true;
        const updatedTabs = ws.tabs.map((t) =>
          t.id === tabId ? { ...t, shellLabel, cwd: cwd || t.cwd } : t,
        );
        next[key] = { ...ws, tabs: updatedTabs };
      }
      return changed ? next : prev;
    });
  }, []);

  const handleTabError = useCallback((tabId: string, message: string) => {
    setErrorsByTab((prev) => ({ ...prev, [tabId]: message }));
  }, []);

  const removeTab = useCallback(
    (tabId: string) => {
      let shouldCollapse = false;
      setWorkspaces((prev) => {
        const next: Record<string, WorkspaceTerminals> = {};
        for (const [key, ws] of Object.entries(prev)) {
          const idx = ws.tabs.findIndex((t) => t.id === tabId);
          if (idx === -1) {
            next[key] = ws;
            continue;
          }
          const remaining = ws.tabs.filter((t) => t.id !== tabId);
          if (remaining.length === 0) {
            if (key === dirKey) {
              shouldCollapse = true;
            }
            continue;
          }
          const nextActive =
            ws.activeId === tabId
              ? (remaining[Math.max(0, idx - 1)]?.id ?? remaining[0].id)
              : ws.activeId;
          next[key] = { tabs: remaining, activeId: nextActive };
        }
        return next;
      });
      setErrorsByTab((prev) => {
        if (!(tabId in prev)) return prev;
        const copy = { ...prev };
        delete copy[tabId];
        return copy;
      });
      if (shouldCollapse) {
        onClose();
      }
    },
    [dirKey, onClose],
  );

  const addTab = () => {
    setWorkspaces((prev) => {
      const ws = prev[dirKey];
      const existingTabs = ws?.tabs ?? [];
      const tab = createTabObject(dirKey, directory, nextTabIndex(existingTabs));
      return {
        ...prev,
        [dirKey]: {
          tabs: [...existingTabs, tab],
          activeId: tab.id,
        },
      };
    });
  };

  const selectTab = (tabId: string) => {
    setWorkspaces((prev) => {
      const ws = prev[dirKey];
      if (!ws || ws.activeId === tabId) return prev;
      return {
        ...prev,
        [dirKey]: { ...ws, activeId: tabId },
      };
    });
  };

  if (!hasOpened) {
    return null;
  }

  const activeWorkspace = workspaces[dirKey];
  const currentTabs = activeWorkspace?.tabs ?? [];
  const activeTabId = activeWorkspace?.activeId ?? "";
  const allTabs = Object.values(workspaces).flatMap((ws) => ws.tabs);
  const activeTab = currentTabs.find((t) => t.id === activeTabId) ?? currentTabs[0];
  const activeError = activeTab ? errorsByTab[activeTab.id] : undefined;

  return (
    <section
      className="terminal-panel"
      data-open={open ? "true" : "false"}
      aria-hidden={!open || undefined}
      aria-label="终端"
      style={open ? { height: `${height}px` } : undefined}
    >
      <div
        className="resizer resizer--top"
        title="拖动调整终端高度，双击复位"
        {...resizer}
      />

      <div className="terminal-panel__head">
        <div className="terminal-panel__tabs" role="tablist" aria-label="终端标签页">
          {currentTabs.map((tab) => {
            const isActive = tab.id === activeTab?.id;
            const title =
              tab.index === 1
                ? `${tab.shellLabel} · ${dirLeafName(tab.cwd)}`
                : `${tab.shellLabel} ${tab.index}`;
            return (
              <div
                key={tab.id}
                className="terminal-tab"
                data-active={isActive ? "true" : undefined}
              >
                <button
                  type="button"
                  role="tab"
                  aria-selected={isActive}
                  className="terminal-tab__btn"
                  onClick={() => selectTab(tab.id)}
                  title={tab.cwd || "终端"}
                >
                  {title}
                </button>
                <button
                  type="button"
                  className="terminal-tab__close"
                  onClick={() => removeTab(tab.id)}
                  title="关闭终端标签页"
                  aria-label="关闭终端标签页"
                >
                  ×
                </button>
              </div>
            );
          })}
          <button
            type="button"
            className="icon-button terminal-panel__add"
            onClick={addTab}
            title="新建终端标签页"
            aria-label="新建终端标签页"
          >
            ＋
          </button>
        </div>

        <span className="terminal-panel__cwd" title={activeTab?.cwd || directory}>
          {activeTab?.cwd || directory}
        </span>

        <button
          type="button"
          className="icon-button"
          onClick={onClose}
          title="收起终端（保持后台会话）"
          aria-label="收起终端"
        >
          ×
        </button>
      </div>

      <div className="terminal-panel__body">
        {activeError ? (
          <div className="terminal-panel__error">{activeError}</div>
        ) : null}
        {allTabs.map((tab) => {
          const isCurrent = open && tab.dirKey === dirKey && tab.id === activeTab?.id && !errorsByTab[tab.id];
          return (
            <div
              key={tab.id}
              className="terminal-panel__slot"
              data-active={isCurrent ? "true" : undefined}
              aria-hidden={!isCurrent || undefined}
            >
              <TerminalSessionView
                tab={tab}
                visible={isCurrent}
                resizing={resizing}
                onMetaChange={handleMetaChange}
                onError={handleTabError}
                onExited={removeTab}
              />
            </div>
          );
        })}
      </div>
    </section>
  );
}
