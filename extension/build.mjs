// 构建 Atlas 浏览器扩展：esbuild 打包各入口，manifest 与静态页原样拷贝，
// 产物落在 dist/（Chrome 里"加载已解压的扩展程序"指向这里）。
import { build } from "esbuild";
import { cpSync, mkdirSync } from "node:fs";

mkdirSync("dist", { recursive: true });

await build({
  entryPoints: ["src/background.ts", "src/content.ts", "src/sidepanel.ts", "src/options.ts"],
  bundle: true,
  format: "esm",
  target: "chrome120",
  outdir: "dist",
  logLevel: "info",
});

for (const file of ["manifest.json", "src/sidepanel.html", "src/options.html", "src/sidepanel.css"]) {
  cpSync(file, `dist/${file.split("/").pop()}`);
}
