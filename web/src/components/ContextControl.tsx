import { useEffect, useState, type RefObject } from "react";
import type { Stats } from "../api";
import { copyText } from "../lib/clipboard";
import { FooterPopover } from "./FooterPopover";

const RING_RADIUS = 6.5;
const RING_CIRCUMFERENCE = 2 * Math.PI * RING_RADIUS;

function contextUsage(stats: Stats | null): {
  ratio: number;
  percent: number;
  tone: "normal" | "warn" | "danger";
} {
  if (!stats || stats.contextWindow <= 0) {
    return { ratio: 0, percent: 0, tone: "normal" };
  }
  const ratio = Math.max(0, Math.min(1, stats.contextTokens / stats.contextWindow));
  const percent = Math.min(100, Math.round(ratio * 100));
  const tone = percent >= 90 ? "danger" : percent >= 70 ? "warn" : "normal";
  return { ratio, percent, tone };
}

function formatCount(value: number): string {
  if (!value) return "0";
  if (value >= 1000) return `${(value / 1000).toFixed(value >= 10000 ? 0 : 1)}k`;
  return String(value);
}

export function ContextRingButton({
  stats,
  open,
  onToggle,
  buttonRef,
}: {
  stats: Stats | null;
  open: boolean;
  onToggle: () => void;
  buttonRef: RefObject<HTMLButtonElement | null>;
}) {
  const { ratio, percent, tone } = contextUsage(stats);
  const visualRatio = ratio > 0 ? Math.max(0.04, ratio) : 0;
  const dashOffset = RING_CIRCUMFERENCE * (1 - visualRatio);

  const title =
    stats && stats.contextWindow > 0
      ? `会话上下文 ${formatCount(stats.contextTokens)} / ${formatCount(stats.contextWindow)} (${percent}%)`
      : stats
        ? `当前会话上下文 ${formatCount(stats.contextTokens)} tokens`
        : "查看会话上下文信息";

  return (
    <button
      ref={buttonRef}
      type="button"
      className="context-ring-btn"
      data-tone={tone}
      data-active={open ? "true" : undefined}
      aria-expanded={open}
      aria-haspopup="dialog"
      aria-label="会话上下文信息"
      title={title}
      onClick={onToggle}
    >
      <svg className="context-ring-svg" viewBox="0 0 18 18" width="16" height="16" aria-hidden="true">
        <circle
          className="context-ring-track"
          cx="9"
          cy="9"
          r={RING_RADIUS}
          fill="none"
          strokeWidth="2.1"
        />
        <circle
          className="context-ring-fill"
          cx="9"
          cy="9"
          r={RING_RADIUS}
          fill="none"
          strokeWidth="2.1"
          strokeLinecap="round"
          strokeDasharray={RING_CIRCUMFERENCE}
          strokeDashoffset={dashOffset}
          transform="rotate(-90 9 9)"
        />
      </svg>
    </button>
  );
}

export function ContextPopover({
  open,
  stats,
  anchorRef,
  onClose,
}: {
  open: boolean;
  stats: Stats | null;
  anchorRef: RefObject<HTMLButtonElement | null>;
  onClose: () => void;
}) {
  const [promptOpen, setPromptOpen] = useState(false);
  const { percent, tone } = contextUsage(stats);

  const mcpConnected = stats?.mcpServers ?? 0;
  const mcpConfigured = stats?.mcpConfigured ?? mcpConnected;
  const mcpTools = stats?.mcpTools ?? 0;
  const mcpTokens = stats?.mcpTokens ?? 0;
  const mcpLabel =
    mcpConfigured > mcpConnected ? `${mcpConnected}/${mcpConfigured} 个` : `${mcpConnected} 个`;

  const skillsCount = stats?.skills ?? 0;
  const skillsTokens = stats?.skillsTokens ?? 0;

  const contextFiles = stats?.contextFiles ?? [];
  const agentsLoaded = Boolean(stats?.agentsMdLoaded);
  const contextFilesTokens = stats?.contextFilesTokens ?? 0;
  const agentsStatus = agentsLoaded
    ? "已加载"
    : contextFiles.length > 0
      ? "未加载（其他规则）"
      : "未加载";

  const systemPromptTokens = stats?.systemPromptTokens ?? 0;
  const hasPrompt = Boolean(stats?.systemPrompt);

  return (
    <>
      <FooterPopover
        open={open}
        anchorRef={anchorRef}
        align="right"
        ariaLabel="会话上下文信息"
        className="context-popover"
        onClose={onClose}
      >
        <div className="footer-popover__head">
          <span className="footer-popover__title">会话上下文</span>
          <span className="context-popover__pct" data-tone={tone}>
            {stats && stats.contextWindow > 0 ? `${percent}%` : stats ? "未设上限" : "读取中…"}
          </span>
        </div>

        {stats ? (
          <div className="context-popover__body">
            <div className="stat">
              <div className="stat__label">
                {stats.contextWindow > 0
                  ? `已用 ${formatCount(stats.contextTokens)} / ${formatCount(stats.contextWindow)} tokens`
                  : `当前上下文 ${formatCount(stats.contextTokens)} tokens`}
              </div>
              <div className="meter">
                <div
                  className="meter__fill context-popover__meter-fill"
                  data-tone={tone}
                  style={{ width: `${stats.contextWindow > 0 ? percent : 0}%` }}
                />
              </div>
            </div>

            <div className="stat-grid">
              <div className="stat">
                <div className="stat__label">MCP</div>
                <div className="stat__value">{mcpLabel}</div>
                <div className="stat__sub">
                  {mcpTools} 工具 · ~{formatCount(mcpTokens)} tok
                </div>
              </div>
              <div className="stat">
                <div className="stat__label">Skill</div>
                <div className="stat__value">{skillsCount} 个</div>
                <div className="stat__sub">~{formatCount(skillsTokens)} tok</div>
              </div>
              <div className="stat" title={contextFiles.length > 0 ? contextFiles.join("\n") : undefined}>
                <div className="stat__label">AGENTS.md</div>
                <div className="stat__value" data-active={agentsLoaded ? "true" : undefined}>
                  {agentsStatus}
                </div>
                <div className="stat__sub">
                  {contextFiles.length > 0
                    ? `${contextFiles.length} 文件 · ~${formatCount(contextFilesTokens)} tok`
                    : "未检测到规则文件"}
                </div>
              </div>
              <div className="stat">
                <div className="stat__label">系统提示词占用</div>
                <div className="stat__value">~{formatCount(systemPromptTokens)} tok</div>
                <div className="stat__sub">含规则与 Skill 目录</div>
              </div>
              <div className="stat">
                <div className="stat__label">消息</div>
                <div className="stat__value">{stats.totalMessages}</div>
              </div>
              <div className="stat">
                <div className="stat__label">工具调用</div>
                <div className="stat__value">{stats.toolCalls}</div>
              </div>
              <div className="stat">
                <div className="stat__label">输入 tokens</div>
                <div className="stat__value">{formatCount(stats.inputTokens)}</div>
              </div>
              <div className="stat">
                <div className="stat__label">输出 tokens</div>
                <div className="stat__value">{formatCount(stats.outputTokens)}</div>
              </div>
              {stats.cost > 0 ? (
                <div className="stat" style={{ gridColumn: "1 / -1" }}>
                  <div className="stat__label">花费</div>
                  <div className="stat__value">${stats.cost.toFixed(4)}</div>
                </div>
              ) : null}
            </div>

            <button
              type="button"
              className="context-popover__prompt-btn"
              disabled={!hasPrompt}
              onClick={() => {
                onClose();
                setPromptOpen(true);
              }}
            >
              <span>系统提示词</span>
              <span className="context-popover__prompt-meta">
                {hasPrompt ? `查看全文 · ~${formatCount(systemPromptTokens)} tok` : "暂无"}
              </span>
            </button>

            {stats.sessionFile ? (
              <div className="context-popover__file" title={stats.sessionFile}>
                {stats.sessionFile}
              </div>
            ) : null}
          </div>
        ) : (
          <div className="field__hint">正在读取当前会话统计…</div>
        )}
      </FooterPopover>

      <SystemPromptDialog
        open={promptOpen}
        prompt={stats?.systemPrompt ?? ""}
        tokens={systemPromptTokens}
        contextFiles={contextFiles}
        onClose={() => setPromptOpen(false)}
      />
    </>
  );
}

function SystemPromptDialog({
  open,
  prompt,
  tokens,
  contextFiles,
  onClose,
}: {
  open: boolean;
  prompt: string;
  tokens: number;
  contextFiles: string[];
  onClose: () => void;
}) {
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (!open) {
      setCopied(false);
      return;
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        onClose();
      }
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [open, onClose]);

  if (!open) return null;

  const copyPrompt = async () => {
    if (!(await copyText(prompt))) return;
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1600);
  };

  return (
    <div
      className="sheet-backdrop"
      role="presentation"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div className="sheet prompt-viewer" role="dialog" aria-modal="true" aria-label="系统提示词">
        <div className="sheet__head">
          <span className="sheet__title">系统提示词</span>
          <span className="prompt-viewer__count">
            ~{formatCount(tokens)} tok · {prompt.length} 字符
          </span>
          <button type="button" className="icon-button" onClick={onClose} aria-label="关闭">
            ✕
          </button>
        </div>

        <div className="sheet__body">
          {contextFiles.length > 0 ? (
            <div className="prompt-viewer__files" title={contextFiles.join("\n")}>
              已加载规则：{contextFiles.join(" · ")}
            </div>
          ) : null}
          <pre className="prompt-viewer__code">{prompt || "（当前会话暂无系统提示词）"}</pre>
        </div>

        <div className="sheet__foot">
          <div className="sheet__actions">
            <button type="button" className="button" disabled={!prompt} onClick={() => void copyPrompt()}>
              {copied ? "已复制" : "复制提示词"}
            </button>
            <button type="button" className="button button--primary" onClick={onClose}>
              关闭
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}
