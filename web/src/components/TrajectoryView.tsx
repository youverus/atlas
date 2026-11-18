import { useState } from "react";
import type { TrajectoryRow, Usage } from "../api";

// 轨迹：同一段会话的另一种读法。
//
// 阅读列回答「它说了什么」，这里回答「到底发生了什么、按什么顺序」。所以每一条
// 记录各占一行、按文件顺序排（seq 就是行号），调用的参数与输出不截断，思考、
// 用量、压缩这些阅读视图故意丢掉的东西都在。默认折叠是因为一次排查要先看见
// 形状，展开才是看内容。
export function TrajectoryView({
  rows,
  loading,
  error,
  onRefresh,
  onClose,
}: {
  rows: TrajectoryRow[];
  loading: boolean;
  error: string;
  onRefresh: () => void;
  onClose: () => void;
}) {
  const [open, setOpen] = useState<number | null>(null);
  const turns = groupByTurn(rows);

  return (
    <div className="trajectory">
      <div className="trajectory__bar">
        <span className="trajectory__title">轨迹</span>
        <span className="trajectory__count">{rows.length} 条记录</span>
        <span className="trajectory__spacer" />
        <button type="button" className="chip chip--button" onClick={onRefresh} disabled={loading}>
          {loading ? "读取中" : "刷新"}
        </button>
        <button
          type="button"
          className="icon-button"
          onClick={onClose}
          title="回到对话"
          aria-label="回到对话"
        >
          ×
        </button>
      </div>

      <div className="trajectory__scroll">
        <div className="trajectory__column">
          {error ? <div className="notice notice--error">{error}</div> : null}
          {!error && rows.length === 0 && !loading ? (
            <div className="trajectory__empty">
              这段会话还没有记录。发一条消息，或者换一段对话再看。
            </div>
          ) : null}

          {turns.map((group) => (
            <div className="traj-turn" key={group.turn}>
              <div className="traj-turn__head">
                {group.turn === 0 ? "会话" : `第 ${group.turn} 轮`}
              </div>
              {group.rows.map((row) => (
                <Row
                  key={row.index}
                  row={row}
                  open={open === row.index}
                  onToggle={() => setOpen(open === row.index ? null : row.index)}
                />
              ))}
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}

function Row({ row, open, onToggle }: { row: TrajectoryRow; open: boolean; onToggle: () => void }) {
  const fields = sections(row);
  return (
    <div className={open ? "traj-row traj-row--open" : "traj-row"} data-kind={row.kind}>
      <button type="button" className="traj-row__head" onClick={onToggle} aria-expanded={open}>
        <span className={"traj-kind traj-kind--" + row.kind}>{label(row)}</span>
        {row.tool ? <span className="traj-row__tool">{row.tool}</span> : null}
        <span className="traj-row__preview">{preview(row)}</span>
        <span className="traj-row__meta">
          {row.isError ? <span className="traj-row__flag">失败</span> : null}
          {row.interrupted ? <span className="traj-row__flag">中断</span> : null}
          {row.durationMs ? <span>{formatMs(row.durationMs)}</span> : null}
          <span className="traj-row__seq" title={`文件第 ${row.seq} 行`}>
            {clock(row.timestamp)}
          </span>
        </span>
      </button>
      {open ? (
        <div className="traj-row__body">
          {fields.map((field) => (
            <div className="traj-field" key={field.label}>
              <div className="traj-field__label">{field.label}</div>
              <pre className="traj-field__value">{field.value}</pre>
            </div>
          ))}
          <div className="traj-field">
            <div className="traj-field__label">出处</div>
            <div className="traj-field__value traj-field__value--line">
              {row.entryId}
              {row.parentId ? ` · 上级 ${row.parentId}` : ""} · 第 {row.seq} 行
            </div>
          </div>
        </div>
      ) : null}
    </div>
  );
}

// label 是这一行的类别名。类别是闭集（服务端 kind），所以这里必须穷举；没有
// 匹配的落到「未知」并按原样显示 kind，一种本 build 叫不出名字的记录是内核的
// 事实，不该被吞掉。
function label(row: TrajectoryRow): string {
  switch (row.kind) {
    case "prompt":
      return "提问";
    case "assistant":
      return "回答";
    case "tool":
      return "调用";
    case "system":
      return "系统";
    case "compaction":
      return "压缩";
    case "branch_summary":
      return "分支摘要";
    case "context_edit":
      return "改写";
    case "model_change":
      return "换模型";
    case "thinking_level_change":
      return "思考档";
    case "label":
      return "标记";
    case "session_info":
      return "会话名";
    case "usage":
      return "用量";
    default:
      return row.kind || "未知";
  }
}

// preview 是折叠时那一行右侧的字。它只取第一行、并且截短：折叠状态要能一眼扫
// 过几十行，而真正的内容在展开之后。
function preview(row: TrajectoryRow): string {
  const text =
    row.kind === "tool"
      ? row.result ?? row.args ?? ""
      : row.text ?? row.thinking ?? row.error ?? row.model ?? "";
  if (!text) {
    // 没有正文的行也要说点什么，否则一列空白会被读成「没记录」。
    if (row.usage) return tokens(row.usage);
    if (row.kind === "compaction" && row.tokensBefore) return `压缩前 ${row.tokensBefore} tokens`;
    return "";
  }
  const first = text.split("\n").find((line) => line.trim() !== "") ?? "";
  const trimmed = first.trim();
  return trimmed.length > 160 ? trimmed.slice(0, 160) + "…" : trimmed;
}

// sections 展开后要显示的内容，按「这一行是什么」决定，而不是把所有字段都摊开：
// 一行里大量空字段会把真正要看的那两行淹掉。
function sections(row: TrajectoryRow): { label: string; value: string }[] {
  const out: { label: string; value: string }[] = [];
  if (row.args) out.push({ label: "参数", value: pretty(row.args) });
  if (row.result) out.push({ label: "结果", value: row.result });
  if (row.text) out.push({ label: "正文", value: row.text });
  if (row.thinking) out.push({ label: "思考", value: row.thinking });
  if (row.error) out.push({ label: "错误", value: row.error });
  if (row.model) out.push({ label: "模型", value: row.model });
  if (row.usage) out.push({ label: "用量", value: usageText(row.usage) });
  if (row.kind === "compaction") {
    const kept = row.ref ? `保留自 ${row.ref}` : "";
    const before = row.tokensBefore ? `压缩前 ${row.tokensBefore} tokens` : "";
    const line = [before, kept].filter(Boolean).join(" · ");
    if (line) out.push({ label: "压缩", value: line });
  }
  if (row.ref && row.kind !== "tool" && row.kind !== "compaction") {
    out.push({ label: "指向", value: row.ref });
  }
  return out;
}

// pretty reformats a JSON argument when it is JSON. A tool that takes plain text
// sends plain text, and re-encoding that would be a lie about what was sent.
function pretty(args: string): string {
  try {
    return JSON.stringify(JSON.parse(args), null, 2);
  } catch {
    return args;
  }
}

function usageText(usage: Usage): string {
  const parts = [
    `输入 ${usage.input}`,
    `输出 ${usage.output}`,
    `合计 ${usage.totalTokens}`,
  ];
  if (usage.cacheRead) parts.push(`缓存读 ${usage.cacheRead}`);
  if (usage.cacheWrite) parts.push(`缓存写 ${usage.cacheWrite}`);
  if (usage.reasoning) parts.push(`推理 ${usage.reasoning}`);
  if (usage.cost.total) parts.push(`$${usage.cost.total.toFixed(4)}`);
  return parts.join(" · ");
}

function tokens(usage: Usage): string {
  return `${usage.totalTokens} tokens`;
}

function formatMs(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  return `${(ms / 1000).toFixed(1)}s`;
}

// clock keeps only the time of day: the file's dates are all the same day in the
// conversations this view exists for, and a full timestamp would push the row's
// own text off the line.
function clock(timestamp: string): string {
  const at = timestamp.indexOf("T");
  if (at < 0) return timestamp;
  const rest = timestamp.slice(at + 1);
  return rest.slice(0, 8);
}

// groupByTurn keeps the file order and starts a new band at every turn. Rows
// recorded before the first prompt (a session naming itself, a model switch at
// the start) belong to turn 0 and are shown first under their own heading.
function groupByTurn(rows: TrajectoryRow[]): { turn: number; rows: TrajectoryRow[] }[] {
  const groups: { turn: number; rows: TrajectoryRow[] }[] = [];
  for (const row of rows) {
    const last = groups[groups.length - 1];
    if (!last || last.turn !== row.turn) {
      groups.push({ turn: row.turn, rows: [row] });
      continue;
    }
    last.rows.push(row);
  }
  return groups;
}
