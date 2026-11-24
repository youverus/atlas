import assert from "node:assert/strict";
import test from "node:test";
import { roleMatches } from "./commands.ts";
import { hasDraggedFiles, readStagedFile, transferredFiles } from "./composerAttachments.ts";
import { matchingPaths, pathInsertion, pathMention } from "./pathCompletion.ts";

test("workspace completion replaces the whole token at the caret", () => {
  const draft = '先看 @"design notes/wire-old.ts" 再继续';
  const caret = draft.indexOf("-old");
  const mention = pathMention(draft, caret);
  assert.deepEqual(mention, {
    start: draft.indexOf("@"),
    end: draft.indexOf(" 再"),
    directory: "design notes/",
    prefix: "wire",
  });
  const { text } = pathInsertion("design notes/wire.ts", false);
  assert.equal(draft.slice(0, mention.start) + text + draft.slice(mention.end), '先看 @"design notes/wire.ts" 再继续');
  assert.equal(pathMention("mail@example.com", 12), null);
  assert.equal(pathMention("@../tmp/", 8), null);
  assert.deepEqual(pathMention("@", 1), { start: 0, end: 1, directory: "", prefix: "" });
  assert.equal(pathMention("看 @main.old，继续", 7).end, 11);
  assert.deepEqual(roleMatches("@exp", 4).items.map((item) => item.name), ["@explorer"]);
});

test("directory insertion keeps quoted paths editable for the next prefix", () => {
  const path = 'notes "draft"/v1\\v2';
  const { text, caretOffset } = pathInsertion(path, true);
  assert.equal(text, '@"notes \\"draft\\"/v1\\\\v2/"');
  assert.equal(text[caretOffset], '"');
  assert.equal(pathMention(text, caretOffset).directory, `${path}/`);
  assert.equal(pathInsertion("src", true).text, "@src/");
  assert.equal(pathInsertion("explorer", false, true).text, '@"explorer"');
});

test("only entries in the requested directory with the typed name prefix are offered", () => {
  const entries = [
    { name: "Docs", path: "src/Docs", isDir: true },
    { name: "diagram.png", path: "src/diagram.png", isDir: false },
    { name: "readme.md", path: "src/readme.md", isDir: false },
  ];
  assert.deepEqual(matchingPaths({ path: "src", entries }, "D"), entries.slice(0, 2));
  assert.deepEqual(matchingPaths({ path: "src", entries }, "diagram."), [entries[1]]);
  assert.deepEqual(matchingPaths(undefined, "d"), []);
});

test("file transfers handle clipboard fallback, folders, and plain text", () => {
  const image = new File([new Uint8Array([137, 80, 78, 71])], "image.png", { type: "image/png" });
  const item = { kind: "file", getAsFile: () => image };
  assert.deepEqual(transferredFiles({ files: [], items: [item] }), { files: [image], hasDirectory: false });
  assert.deepEqual(transferredFiles({ files: [image], items: [item] }), { files: [image], hasDirectory: false });
  const directory = {
    kind: "file",
    webkitGetAsEntry: () => ({ isDirectory: true }),
    getAsFile: () => { throw new Error("directory read"); },
  };
  assert.deepEqual(transferredFiles({ files: [], items: [directory, item] }), { files: [image], hasDirectory: true });
  const plain = { types: ["text/plain"], files: [], items: [{ kind: "string" }] };
  assert.equal(hasDraggedFiles(plain), false);
  assert.deepEqual(transferredFiles(plain), { files: [], hasDirectory: false });
  assert.equal(hasDraggedFiles({ types: ["Files"], files: [], items: [] }), true);
});

test("attachment staging infers payloads and applies chooser validation", async () => {
  const image = new File([new Uint8Array([137, 80, 78, 71])], "pasted.PNG", { type: "application/octet-stream" });
  const { id: imageId, ...payload } = await readStagedFile(image);
  assert.ok(imageId);
  assert.deepEqual(payload, {
    kind: "image", name: "pasted.PNG", mimeType: "image/png", size: 4, data: "iVBORw==",
    previewUrl: "data:image/png;base64,iVBORw==",
  });
  const pdf = new File(["%PDF-1.7"], "report.pdf");
  const { id: firstId, ...inferred } = await readStagedFile(pdf);
  const { id: secondId, ...chosen } = await readStagedFile(pdf, "pdf");
  assert.notEqual(firstId, secondId);
  assert.deepEqual(inferred, chosen);
  assert.equal((await readStagedFile(new File(["内容"], "README"))).data, "内容");
  await assert.rejects(readStagedFile(new File([new Uint8Array([0, 255])], "archive.zip")), /二进制/);
  await assert.rejects(readStagedFile(new File([new Uint8Array(5 * 1024 * 1024 + 1)], "large.gif")), /上限/);
});
