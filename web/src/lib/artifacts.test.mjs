import assert from "node:assert/strict";
import test from "node:test";
import {
  artifactExtension,
  artifactKindLabel,
  collectTurnArtifacts,
  defaultExportFilename,
  formatArtifactBytes,
  parseCsv,
  sortCsvRows,
  wrapArtifactHtml,
} from "./artifacts.ts";

test("artifact labels, extensions, and byte formatting", () => {
  assert.equal(artifactKindLabel("html"), "HTML");
  assert.equal(artifactKindLabel("code", "typescript"), "TYPESCRIPT");
  assert.equal(artifactExtension("markdown"), "md");
  assert.equal(artifactExtension("code", "python"), "py");
  assert.equal(defaultExportFilename("chart-1", "csv"), "artifacts/chart-1.csv");
  assert.equal(formatArtifactBytes(512), "512 B");
  assert.equal(formatArtifactBytes(2048), "2.0 KB");
});

test("collectTurnArtifacts deduplicates by artifact id keeping latest version", () => {
  const out = collectTurnArtifacts([
    { artifact: { id: "dash", kind: "html", title: "Dash v1", version: 1 } },
    { artifact: { id: "err-doc", kind: "markdown", title: "Err", version: 1 }, error: true },
    { artifact: { id: "data", kind: "csv", title: "Metrics", version: 1 } },
    { artifact: { id: "dash", kind: "html", title: "Dash v2", version: 2 } },
  ]);
  assert.deepEqual(
    out.map((item) => [item.id, item.version, item.title]),
    [
      ["dash", 2, "Dash v2"],
      ["data", 1, "Metrics"],
    ],
  );
});

test("parseCsv and sortCsvRows handle RFC-4180 quotes, newlines, and numeric order", () => {
  const raw = '\uFEFFName,Score,Note\r\n"Alice, A.",9.5,"Line 1\nLine ""2"""\r\nBob,10,ok\r\nCarol,2,low\r\n';
  const parsed = parseCsv(raw);
  assert.deepEqual(parsed.headers, ["Name", "Score", "Note"]);
  assert.equal(parsed.rows.length, 3);
  assert.equal(parsed.rows[0][0], "Alice, A.");
  assert.equal(parsed.rows[0][2], 'Line 1\nLine "2"');

  const asc = sortCsvRows(parsed.rows, 1, "asc");
  assert.deepEqual(
    asc.map((r) => r[0]),
    ["Carol", "Alice, A.", "Bob"],
  );
  const desc = sortCsvRows(parsed.rows, 1, "desc");
  assert.deepEqual(
    desc.map((r) => r[0]),
    ["Bob", "Alice, A.", "Carol"],
  );
});

test("wrapArtifactHtml injects strict CSP and theme variables for fragments and full docs", () => {
  const frag = wrapArtifactHtml("<div>Hello</div>", false);
  assert.match(frag, /<!doctype html>/i);
  assert.match(frag, /connect-src 'none'/);
  assert.match(frag, /form-action 'none'/);
  assert.match(frag, /base-uri 'none'/);
  assert.match(frag, /--bg:#ffffff;/);
  assert.ok(frag.includes("<body><div>Hello</div></body>"));

  const full = wrapArtifactHtml("<!DOCTYPE html><html><head><title>X</title></head><body>Hi</body></html>", true);
  assert.match(full, /<head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy"/);
  assert.match(full, /--bg:#161616;/);
  assert.ok(full.includes("<title>X</title>"));
});
