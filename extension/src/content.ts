// 划词浮动条：选中页面文字后浮出「解释 / 翻译 / 总结」，点击后交给
// background 处理（开面板 + 发问）。脚本只观察选择事件，不向任何服务
// 发送数据——没有点击就没有页面内容离开页面。
//
// UI 放进 shadow root，页面样式进不来，浮动条也不污染页面。

const HOST_ID = "atlas-selection-bar";

interface BarHandle {
  host: HTMLElement;
  bar: HTMLElement;
}

let bar: BarHandle | null = null;

function buildBar(): BarHandle {
  const host = document.createElement("div");
  host.id = HOST_ID;
  const shadow = host.attachShadow({ mode: "closed" });
  const style = document.createElement("style");
  style.textContent = `
    .bar {
      position: fixed;
      z-index: 2147483647;
      display: none;
      gap: 2px;
      padding: 3px;
      border-radius: 10px;
      background: #1f2126;
      box-shadow: 0 4px 16px rgba(0,0,0,.28);
      font: 12px/1 -apple-system, "PingFang SC", "Segoe UI", sans-serif;
    }
    .bar button {
      border: none;
      background: transparent;
      color: #e8e9ec;
      font: inherit;
      padding: 6px 10px;
      border-radius: 7px;
      cursor: pointer;
    }
    .bar button:hover { background: rgba(255,255,255,.14); }
  `;
  const row = document.createElement("div");
  row.className = "bar";
  for (const [action, label] of [
    ["explain", "解释"],
    ["translate", "翻译"],
    ["summarize", "总结"],
  ] as const) {
    const button = document.createElement("button");
    button.type = "button";
    button.textContent = label;
    button.addEventListener("click", () => {
      const text = selectedText();
      hide();
      if (!text) return;
      void chrome.runtime.sendMessage({ type: "quick-ask", action, text }).catch(() => {});
    });
    row.append(button);
  }
  shadow.append(style, row);
  document.documentElement.append(host);
  return { host, bar: row };
}

function selectedText(): string {
  const selection = window.getSelection();
  return selection ? selection.toString().trim() : "";
}

function show(): void {
  if (!bar) bar = buildBar();
  const range = window.getSelection()?.rangeCount
    ? window.getSelection()!.getRangeAt(0).getBoundingClientRect()
    : null;
  if (!range || (range.width === 0 && range.height === 0)) {
    hide();
    return;
  }
  // fixed 定位用的是视口坐标：浮条横向居中在选择区下方 8px，贴不下的
  // 时候翻到选择区上方。
  const BAR_WIDTH = 150;
  const BAR_HEIGHT = 36;
  const left = Math.max(8, Math.min(range.left + range.width / 2 - BAR_WIDTH / 2, window.innerWidth - BAR_WIDTH - 8));
  let top = range.bottom + 8;
  if (top + BAR_HEIGHT > window.innerHeight - 8) top = Math.max(8, range.top - BAR_HEIGHT - 8);
  bar.bar.style.left = `${left}px`;
  bar.bar.style.top = `${top}px`;
  bar.bar.style.display = "inline-flex";
}

function hide(): void {
  if (bar) bar.bar.style.display = "none";
}

// mouseup 是划词的终点；keyup 覆盖 Shift+方向键。文档按下即收起，避免
// 上一轮选择的浮条挡住新一轮划词的起点。
document.addEventListener("mouseup", () => {
  const text = selectedText();
  if (text && !isEditableSelection()) show();
  else hide();
});
document.addEventListener("keyup", () => {
  if (selectedText() && !isEditableSelection()) show();
});
document.addEventListener("mousedown", (event) => {
  if (!bar) return;
  const path = event.composedPath();
  if (!path.includes(bar.host)) hide();
});
window.addEventListener("scroll", hide, { passive: true });
window.addEventListener("resize", hide);

// 输入框里的选择多半是在编辑自己的文字，弹条反而碍事。
function isEditableSelection(): boolean {
  const node = window.getSelection()?.anchorNode;
  const element = node instanceof HTMLElement ? node : node?.parentElement;
  if (!element) return false;
  return element.isContentEditable || element.closest("input, textarea, [contenteditable]") !== null;
}
