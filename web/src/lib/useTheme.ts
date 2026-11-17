// Theme: three states, not a boolean.
//
// "system" is the default and has to stay distinguishable from an explicit
// light choice, otherwise a reader who picked light once ends up stuck on it
// after their machine switches to dark at night.

import { useCallback, useEffect, useState } from "react";

export type Theme = "system" | "light" | "dark";

const KEY = "atlas.theme";

function read(): Theme {
  const stored = localStorage.getItem(KEY);
  return stored === "light" || stored === "dark" ? stored : "system";
}

function paint(theme: Theme): void {
  const dark =
    theme === "dark" ||
    (theme === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches);
  document.documentElement.classList.toggle("dark", dark);
}

export function useTheme(): [Theme, (next: Theme) => void] {
  const [theme, setTheme] = useState<Theme>(read);

  useEffect(() => {
    paint(theme);
    localStorage.setItem(KEY, theme);
    if (theme !== "system") return;
    // Only "system" follows the machine; an explicit choice is a promise.
    const query = window.matchMedia("(prefers-color-scheme: dark)");
    const listener = () => paint(theme);
    query.addEventListener("change", listener);
    return () => query.removeEventListener("change", listener);
  }, [theme]);

  const set = useCallback((next: Theme) => setTheme(next), []);
  return [theme, set];
}
