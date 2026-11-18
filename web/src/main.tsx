import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import App from "./App";

// KaTeX first: its stylesheet is the vendor baseline for formulas, and every
// sheet below is one of ours, allowed to override it.
import "katex/dist/katex.min.css";

// Order matters: tokens define the vocabulary, then the frame, then the
// contents of the frame.
import "./styles/tokens.css";
import "./styles/shell.css";
import "./styles/chat.css";
import "./styles/panels.css";
import "./styles/markdown.css";

const root = document.getElementById("root");
if (!root) throw new Error("atlas: #root is missing from index.html");

createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
