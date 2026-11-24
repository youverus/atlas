import type { ITheme } from "@xterm/xterm";

const LIGHT_ANSI: Omit<ITheme, "background" | "foreground" | "cursor" | "cursorAccent" | "selectionBackground"> = {
  black: "#1f2937",
  red: "#dc2626",
  green: "#16a34a",
  yellow: "#ca8a04",
  blue: "#2563eb",
  magenta: "#9333ea",
  cyan: "#0891b2",
  white: "#e5e7eb",
  brightBlack: "#6b7280",
  brightRed: "#ef4444",
  brightGreen: "#22c55e",
  brightYellow: "#eab308",
  brightBlue: "#3b82f6",
  brightMagenta: "#a855f7",
  brightCyan: "#06b6d4",
  brightWhite: "#f9fafb",
};

const DARK_ANSI: Omit<ITheme, "background" | "foreground" | "cursor" | "cursorAccent" | "selectionBackground"> = {
  black: "#1f2937",
  red: "#ef4444",
  green: "#22c55e",
  yellow: "#eab308",
  blue: "#3b82f6",
  magenta: "#a855f7",
  cyan: "#06b6d4",
  white: "#e5e7eb",
  brightBlack: "#6b7280",
  brightRed: "#f87171",
  brightGreen: "#4ade80",
  brightYellow: "#facc15",
  brightBlue: "#60a5fa",
  brightMagenta: "#c084fc",
  brightCyan: "#22d3ee",
  brightWhite: "#f9fafb",
};

/** Reads the active light/dark `--fx-*` tokens into an xterm.js ITheme. */
export function readAtlasTerminalTheme(): ITheme {
  const dark = typeof document !== "undefined" && document.documentElement.classList.contains("dark");
  if (typeof document === "undefined") {
    return {
      background: dark ? "#1d1d20" : "#f7f7f7",
      foreground: dark ? "#f3f3f3" : "#171717",
      cursor: dark ? "#f3f3f3" : "#171717",
      cursorAccent: dark ? "#1d1d20" : "#f7f7f7",
      selectionBackground: dark ? "rgba(59, 130, 246, 0.28)" : "rgba(37, 99, 235, 0.2)",
      ...(dark ? DARK_ANSI : LIGHT_ANSI),
    };
  }
  const style = getComputedStyle(document.documentElement);
  const bg = style.getPropertyValue("--fx-surface-inset").trim() || (dark ? "#1d1d20" : "#f3f3f3");
  const fg = style.getPropertyValue("--fx-text-primary").trim() || (dark ? "#f3f3f3" : "#171717");
  const selection =
    style.getPropertyValue("--fx-accent-blue-selection").trim() ||
    (dark ? "rgba(59, 130, 246, 0.28)" : "rgba(37, 99, 235, 0.2)");

  return {
    background: bg,
    foreground: fg,
    cursor: fg,
    cursorAccent: bg,
    selectionBackground: selection,
    ...(dark ? DARK_ANSI : LIGHT_ANSI),
  };
}
