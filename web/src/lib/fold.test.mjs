import assert from "node:assert/strict";
import test from "node:test";
import { apply, blank, interrupt, replay } from "./fold.ts";

function modelFinished(fields) {
  return { type: "message_end", message: { role: "assistant", timestamp: 2500, ...fields } };
}

const failures = [
  ["dial tcp 127.0.0.1:9: connect: connection refused", "无法连接模型服务"],
  ["lookup provider.example: no such host", "无法解析模型服务地址"],
  ["net/http: timeout awaiting response headers", "等待模型服务响应超时"],
  ["model response stream idle timeout after 45s", "模型服务已连接，但响应流长时间没有新内容"],
  ["401 Unauthorized: invalid API key", "HTTP 401 · 模型服务认证失败"],
  ["403 Forbidden", "HTTP 403 · 模型服务拒绝访问"],
  ["404 Not Found", "HTTP 404 · 找不到模型或 API 地址"],
  ["429 Too Many Requests: rate_limit_exceeded", "HTTP 429 · 模型请求达到频率限制"],
  ["429 Too Many Requests: insufficient_quota", "HTTP 429 · 模型服务的额度或余额不足"],
  ["503 Service Unavailable", "HTTP 503 · 模型服务暂时不可用"],
  ["openai: stream ended without finish_reason", "模型回复中途断开"],
  ["", "模型调用失败"],
];

for (const [detail, expected] of failures) {
  test(`displays ${expected} in live and replayed chat`, () => {
    const result = apply([blank("user"), blank("answer", { text: "部分回复", open: true })], modelFinished({
      stopReason: "error",
      errorMessage: detail,
    })).blocks;
    assert.equal(result[1].text, "部分回复");
    assert.equal(result[1].open, false);
    const error = result[2];
    assert.equal(error.role, "error");
    assert.ok(error.text.startsWith(expected), error.text);
    assert.equal(error.endedAt, 2500);
    assert.equal(replay([{ role: "error", text: detail }])[0].text, error.text);
    assert.equal(apply(result, { type: "agent_end" }).blocks.length, result.length);
  });
}

test("does not display an error for a successful or cancelled response", () => {
  const blocks = [blank("user"), blank("answer", { text: "回复", open: true })];
  const success = apply(blocks, modelFinished({ stopReason: "stop" })).blocks;
  const cancelled = apply(blocks, modelFinished({ stopReason: "aborted", errorMessage: "request aborted" })).blocks;
  for (const result of [success, cancelled]) {
    assert.deepEqual(result.map((block) => block.role), ["user", "answer"]);
    assert.equal(result[1].open, false);
  }
});

test("keeps a single stopped notice when the saved transcript arrives before the final status", () => {
  const blocks = replay([{ role: "answer", text: "部分回复" }, { role: "notice", text: "interrupted" }]);
  assert.deepEqual(interrupt(blocks).map(({ text }) => text), ["部分回复", "已停止本轮"]);
});

test("only clears a context overflow error if compaction will retry it", () => {
  const failure = modelFinished({ stopReason: "error", errorMessage: "400 Bad Request", errorCode: "context_overflow" });
  const failed = apply([blank("user")], failure).blocks;
  assert.match(failed[1].text, /消息超出模型的上下文上限/);
  assert.equal(replay([{ role: "error", text: "400 Bad Request", errorCode: "context_overflow" }])[0].text, failed[1].text);
  const compacting = apply(failed, { type: "compaction_start", reason: "overflow" }).blocks;
  const recovered = apply(compacting, { type: "compaction_end", reason: "overflow", willRetry: true }).blocks;
  assert.ok(!recovered.some((block) => block.role === "error"));
  const exhausted = apply(compacting, { type: "compaction_end", reason: "overflow", willRetry: false }).blocks;
  assert.ok(exhausted.some((block) => block.role === "error"));
});

test("captures artifact receipt from live tool_execution_end and persisted replay", () => {
  const started = apply([blank("user")], {
    type: "tool_execution_start",
    toolCallId: "tc-art-1",
    toolName: "artifact_publish",
    args: { id: "sales-dash", kind: "html" },
  }).blocks;

  const published = apply(started, {
    type: "tool_execution_end",
    toolCallId: "tc-art-1",
    toolName: "artifact_publish",
    isError: false,
    result: {
      content: [{ type: "text", text: "Published artifact sales-dash" }],
      details: {
        id: "sales-dash",
        kind: "html",
        title: "Sales Dashboard",
        version: 2,
        bytes: 420,
        contentType: "text/html",
        action: "publish",
      },
    },
  }).blocks;

  assert.equal(published[1].artifact?.id, "sales-dash");
  assert.equal(published[1].artifact?.version, 2);
  assert.equal(published[1].artifact?.title, "Sales Dashboard");

  // Failed or non-artifact tools must not attach artifact receipts.
  const failed = apply([blank("user")], {
    type: "tool_execution_end",
    toolCallId: "tc-art-err",
    toolName: "artifact_publish",
    isError: true,
    result: {
      content: [{ type: "text", text: "boom" }],
      details: { id: "bad", kind: "html", version: 1 },
    },
  }).blocks;
  assert.equal(failed[1].artifact, undefined);

  const replayed = replay([
    {
      role: "tool",
      name: "artifact_patch",
      id: "tc-art-2",
      note: "done",
      artifact: { id: "sales-dash", kind: "html", title: "Sales Dashboard v3", version: 3 },
    },
  ]);
  assert.equal(replayed[0].artifact?.id, "sales-dash");
  assert.equal(replayed[0].artifact?.version, 3);
});

