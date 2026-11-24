// 快捷动作的 prompt 模板：右键菜单与划词浮动条共用同一份，保证两条入口
// 对同一行为产生一致的问题。

export type QuickAction = "explain" | "translate" | "summarize" | "summarize_page";

export const QUICK_LABELS: Record<QuickAction, string> = {
  explain: "解释",
  translate: "翻译",
  summarize: "总结所选",
  summarize_page: "总结本页",
};

const PROMPTS: Record<QuickAction, (text: string) => string> = {
  explain: (text) => `请解释下面这段内容的含义，必要时补充背景：\n\n${text}`,
  translate: (text) =>
    `请翻译下面的内容：原文是中文时译成英文，否则译成中文。只输出译文。\n\n${text}`,
  summarize: (text) => `请总结下面这段内容的核心要点：\n\n${text}`,
  summarize_page: (text) =>
    `请总结下面这个网页的核心内容，用要点列出主要事实与结论。\n\n${text}`,
};

export function quickPrompt(action: QuickAction, text: string): string {
  const compose = PROMPTS[action];
  return compose ? compose(text) : text;
}

// 日志里的用户条目用短标签：完整 prompt 对话滚动里不可读。
export function quickUserLabel(action: QuickAction, text: string): string {
  const label = QUICK_LABELS[action] ?? action;
  const brief = text.length > 80 ? `${text.slice(0, 80)}…` : text;
  return `${label}：${brief.replace(/\s+/g, " ")}`;
}
