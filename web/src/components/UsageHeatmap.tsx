import { useId, useLayoutEffect, useRef, useState } from "react";
import type { CSSProperties } from "react";

const LEVELS = [0, 1, 1_001, 10_000, 100_000, 1_000_000];
const DAYS_PER_WEEK = 7;

export function formatTokens(value: number): string {
  if (value <= 1_000) return String(value);
  const unit = value >= 1_000_000 ? 1_000_000 : 1_000;
  const count = Math.floor(value / (unit / 10)) / 10;
  return `${count}${unit === 1_000_000 ? "m" : "k"}`;
}

function localDate(date: Date): string {
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");
  return `${date.getFullYear()}-${month}-${day}`;
}

function heatmapPeriod(today: Date, offset: number, usage: Record<string, number>) {
  const year = today.getFullYear();
  const month = today.getMonth() - offset * 12;
  const firstDay = new Date(year, month - 11, 1, 12);
  const lastDay = offset === 0 ? new Date(year, month, today.getDate(), 12) : new Date(year, month + 1, 0, 12);
  const leading = (firstDay.getDay() + DAYS_PER_WEEK - 1) % DAYS_PER_WEEK;
  const days: { key: string; tokens: number; row: number; column: number }[] = [];
  const months: { key: string; month: number; column: number }[] = [];
  let total = 0;
  for (const date = new Date(firstDay); date <= lastDay; date.setDate(date.getDate() + 1)) {
    const key = localDate(date);
    const tokens = usage[key] ?? 0;
    const index = leading + days.length;
    const column = Math.floor(index / DAYS_PER_WEEK) + 1;
    const row = (index % DAYS_PER_WEEK) + 1;
    days.push({ key, tokens, row, column });
    total += tokens;
    if (date.getDate() === 1) months.push({ key, month: date.getMonth() + 1, column });
  }
  return { firstDay, lastDay, days, months, total, columns: Math.ceil((leading + days.length) / DAYS_PER_WEEK) };
}

export function UsageHeatmap({ usage, historyNavigation = false }: {
  usage: Record<string, number>;
  historyNavigation?: boolean;
}) {
  const titleId = useId();
  const scroller = useRef<HTMLDivElement>(null);
  const [offset, setOffset] = useState(0);
  const [selectedDate, setSelectedDate] = useState("");
  const [focusedDate, setFocusedDate] = useState("");
  const { firstDay, lastDay, days, months, total, columns } = heatmapPeriod(new Date(), offset, usage);
  const canGoBack = Object.keys(usage).some((date) => date < localDate(firstDay));
  const dateLabel = `${firstDay.getFullYear()}年${firstDay.getMonth() + 1}月 – ${lastDay.getFullYear()}年${lastDay.getMonth() + 1}月`;
  const selected = days.find((day) => day.key === selectedDate);
  const focused = days.find((day) => day.key === focusedDate) ?? days[days.length - 1];

  useLayoutEffect(() => {
    const node = scroller.current;
    if (node) node.scrollLeft = node.scrollWidth;
  }, [offset, columns]);

  const move = (direction: number) => {
    setOffset((current) => current + direction);
    setSelectedDate("");
    setFocusedDate("");
  };

  return (
    <section className="usage-heatmap" aria-labelledby={titleId}>
      <div className="usage-heatmap__header">
        <div>
          <h2 id={titleId}>Token 用量</h2>
          <div className="usage-heatmap__range" aria-live="polite">{dateLabel}</div>
        </div>
        <div className="usage-heatmap__actions">
          <span className="usage-heatmap__total" aria-label={`区间合计 ${formatTokens(total)} Token`}>
            {formatTokens(total)} Token
          </span>
          {historyNavigation ? (
            <div className="usage-heatmap__navigation">
              <button type="button" className="button" aria-label="往前 12 个月" onClick={() => move(1)} disabled={!canGoBack}>‹</button>
              <button type="button" className="button" aria-label="往后 12 个月" onClick={() => move(-1)} disabled={offset === 0}>›</button>
            </div>
          ) : null}
        </div>
      </div>
      <div className="usage-heatmap__scroll" ref={scroller}>
        <div
          className="usage-heatmap__grid"
          style={{ "--heatmap-weeks": columns } as CSSProperties}
          role="group"
          aria-label="每日 Token 用量热力图"
          onKeyDown={(event) => {
            const index = days.findIndex((day) => day.key === focused.key);
            const targets: Record<string, number> = {
              ArrowLeft: index - DAYS_PER_WEEK,
              ArrowRight: index + DAYS_PER_WEEK,
              ArrowUp: index - 1,
              ArrowDown: index + 1,
              Home: 0,
              End: days.length - 1,
            };
            const target = targets[event.key];
            if (target === undefined) return;
            event.preventDefault();
            const day = days[target];
            if (day) scroller.current?.querySelector<HTMLButtonElement>(`[data-date="${day.key}"]`)?.focus();
          }}
        >
          {days.map(({ key, tokens, row, column }) => {
            const label = `${key} · ${formatTokens(tokens)} Token`;
            return (
              <button
                type="button"
                key={key}
                className="usage-heatmap__day"
                data-date={key}
                data-level={LEVELS.filter((threshold) => tokens >= threshold).length - 1}
                style={{ gridRow: row, gridColumn: column }}
                title={label}
                aria-label={label}
                aria-pressed={selectedDate === key}
                tabIndex={focused.key === key ? 0 : -1}
                onFocus={() => setFocusedDate(key)}
                onClick={() => setSelectedDate(key)}
              />
            );
          })}
          {months.map(({ key, month, column }) => (
            <span key={key} className="usage-heatmap__month" style={{ gridColumn: column }}>{month}月</span>
          ))}
        </div>
      </div>
      <div className="usage-heatmap__footer">
        <span aria-live="polite">
          {selected ? `${selected.key} · ${formatTokens(selected.tokens)} Token` : total === 0 ? "此区间暂无用量" : "选择方格查看当天用量"}
        </span>
        <div className="usage-heatmap__legend" aria-label="颜色越深，用量越高">
          <span>少</span>
          {LEVELS.map((tokens, level) => <span key={tokens} data-level={level} aria-hidden="true" />)}
          <span>多</span>
        </div>
      </div>
    </section>
  );
}
