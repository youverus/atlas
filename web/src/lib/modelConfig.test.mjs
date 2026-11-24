import assert from "node:assert/strict";
import test from "node:test";
import {
  DEEPSEEK_ANTHROPIC_BASE_URL,
  DEEPSEEK_OPENAI_BASE_URL,
  adaptBaseUrlForProtocol,
  baseUrlPlaceholder,
  createDeepSeekProviderPreset,
  diagnoseProviderConfig,
  normalizeAnthropicBaseUrl,
  serializeForSave,
  validateDraft,
} from "./modelConfig.ts";

let seq = 0;
const makeKey = (prefix) => `${prefix}-${++seq}`;

test("createDeepSeekProviderPreset builds official DeepSeek preset with unique names", () => {
  const first = createDeepSeekProviderPreset([], makeKey);
  assert.equal(first.name, "deepseek");
  assert.equal(first.api, "openai-completions");
  assert.equal(first.baseUrl, DEEPSEEK_OPENAI_BASE_URL);
  assert.equal(first.apiKeyEnv, "DEEPSEEK_API_KEY");
  assert.deepEqual(first.compat, {
    thinkingDialect: "deepseek",
    requireReasoningContent: true,
  });
  assert.deepEqual(
    first.models.map((m) => m.id),
    ["deepseek-flash", "deepseek-v4-pro"],
  );

  const second = createDeepSeekProviderPreset([first], makeKey);
  assert.equal(second.name, "deepseek-2");
});

test("adaptBaseUrlForProtocol translates api.deepseek.com endpoints across protocols", () => {
  assert.equal(
    adaptBaseUrlForProtocol("https://api.deepseek.com/v1", "anthropic-messages"),
    DEEPSEEK_ANTHROPIC_BASE_URL,
  );
  assert.equal(
    adaptBaseUrlForProtocol("https://api.deepseek.com", "anthropic-messages"),
    DEEPSEEK_ANTHROPIC_BASE_URL,
  );
  assert.equal(
    adaptBaseUrlForProtocol("https://api.deepseek.com/anthropic/", "anthropic-messages"),
    DEEPSEEK_ANTHROPIC_BASE_URL,
  );
  assert.equal(
    adaptBaseUrlForProtocol("https://api.deepseek.com/anthropic/v1", "openai-completions"),
    DEEPSEEK_OPENAI_BASE_URL,
  );
  assert.equal(
    adaptBaseUrlForProtocol("https://api.deepseek.com/anthropic", "openai-completions"),
    DEEPSEEK_OPENAI_BASE_URL,
  );
  // Third-party relay /v1 URLs remain /v1; SDK paths gain /v1 under anthropic-messages.
  assert.equal(
    adaptBaseUrlForProtocol("https://proxy.example.com/v1", "anthropic-messages"),
    "https://proxy.example.com/v1",
  );
  assert.equal(
    adaptBaseUrlForProtocol("https://open.bigmodel.cn/api/anthropic", "anthropic-messages"),
    "https://open.bigmodel.cn/api/anthropic/v1",
  );
});

test("normalizeAnthropicBaseUrl normalizes any anthropic-messages host and path", () => {
  assert.equal(normalizeAnthropicBaseUrl(""), "");
  assert.equal(normalizeAnthropicBaseUrl("https://api.anthropic.com"), "https://api.anthropic.com/v1");
  assert.equal(normalizeAnthropicBaseUrl("https://api.anthropic.com/v1/messages"), "https://api.anthropic.com/v1");
  assert.equal(normalizeAnthropicBaseUrl("https://api.minimaxi.com/anthropic"), "https://api.minimaxi.com/anthropic/v1");
  assert.equal(
    normalizeAnthropicBaseUrl("https://dashscope.aliyuncs.com/apps/anthropic/"),
    "https://dashscope.aliyuncs.com/apps/anthropic/v1",
  );
  assert.equal(normalizeAnthropicBaseUrl("https://api.kimi.com/coding"), "https://api.kimi.com/coding/v1");
  assert.equal(normalizeAnthropicBaseUrl("https://openrouter.ai/api"), "https://openrouter.ai/api/v1");
  assert.equal(normalizeAnthropicBaseUrl("http://localhost:4000"), "http://localhost:4000/v1");
  assert.equal(normalizeAnthropicBaseUrl("https://proxy.example.com/custom-v2"), "https://proxy.example.com/custom-v2");
});

test("baseUrlPlaceholder reflects protocol and deepseek provider", () => {
  assert.ok(baseUrlPlaceholder("anthropic-messages", "deepseek").includes(DEEPSEEK_ANTHROPIC_BASE_URL));
  assert.ok(baseUrlPlaceholder("openai-completions", "deepseek").includes(DEEPSEEK_OPENAI_BASE_URL));
  assert.ok(baseUrlPlaceholder("anthropic-messages", "claude").includes("api.anthropic.com"));
});

test("serializeForSave trims apiKey and strips ui-only apiKeyEnvSet", () => {
  const payload = serializeForSave({
    configPath: "/tmp/config.json",
    defaultModel: " deepseek/deepseek-flash ",
    thinkingLevel: " high ",
    providers: [
      {
        _key: "prov-1",
        name: " deepseek ",
        api: " openai-completions ",
        baseUrl: " https://api.deepseek.com/v1 ",
        apiKey: "  sk-deepseek-secret\n ",
        apiKeyEnv: " DEEPSEEK_API_KEY ",
        apiKeyEnvSet: true,
        userAgent: " ",
        models: [
          {
            _key: "model-1",
            id: " deepseek-flash ",
            name: " DeepSeek Flash ",
            contextWindow: 128000,
            maxTokens: 8192,
            reasoning: true,
            thinkingLevels: ["low", "high"],
            input: ["text"],
          },
        ],
      },
    ],
  });

  assert.equal(payload.defaultModel, "deepseek/deepseek-flash");
  assert.equal(payload.thinkingLevel, "high");
  assert.equal(payload.providers[0].name, "deepseek");
  assert.equal(payload.providers[0].apiKey, "sk-deepseek-secret");
  assert.equal(payload.providers[0].baseUrl, "https://api.deepseek.com/v1");
  assert.equal("apiKeyEnvSet" in payload.providers[0], false);
  assert.equal(validateDraft(payload), null);
});

test("diagnoseProviderConfig flags DeepSeek 404 URL mismatches, missing URL, and env shadowing", () => {
  const anthropicWrongPath = diagnoseProviderConfig({
    name: "deepseek",
    api: "anthropic-messages",
    baseUrl: "https://api.deepseek.com/v1",
    apiKey: "sk-123",
    apiKeyEnv: "DEEPSEEK_API_KEY",
    apiKeyEnvSet: false,
    models: [{ id: "deepseek-flash" }],
  });
  assert.equal(anthropicWrongPath.length, 1);
  assert.equal(anthropicWrongPath[0].level, "warning");
  assert.equal(anthropicWrongPath[0].suggestedBaseUrl, DEEPSEEK_ANTHROPIC_BASE_URL);

  const emptyDeepSeekURL = diagnoseProviderConfig({
    name: "deepseek",
    api: "openai-completions",
    baseUrl: "",
    apiKey: "",
    apiKeyEnv: "DEEPSEEK_API_KEY",
    apiKeyEnvSet: false,
    models: [{ id: "deepseek-flash" }],
  });
  assert.equal(emptyDeepSeekURL[0].suggestedBaseUrl, DEEPSEEK_OPENAI_BASE_URL);
  assert.ok(emptyDeepSeekURL.some((item) => item.message.includes("401")));

  const envShadow = diagnoseProviderConfig({
    name: "deepseek",
    api: "openai-completions",
    baseUrl: DEEPSEEK_OPENAI_BASE_URL,
    apiKey: "sk-typed",
    apiKeyEnv: "DEEPSEEK_API_KEY",
    apiKeyEnvSet: true,
    models: [{ id: "deepseek-flash" }],
  });
  assert.equal(envShadow.length, 1);
  assert.equal(envShadow[0].level, "warning");
  assert.ok(envShadow[0].message.includes("DEEPSEEK_API_KEY"));

  const genericAnthropic = diagnoseProviderConfig({
    name: "zhipu",
    api: "anthropic-messages",
    baseUrl: "https://open.bigmodel.cn/api/anthropic",
    apiKey: "sk-zhipu",
    models: [{ id: "glm-4.7" }],
  });
  assert.equal(genericAnthropic.length, 1);
  assert.equal(genericAnthropic[0].level, "info");
  assert.equal(genericAnthropic[0].suggestedBaseUrl, "https://open.bigmodel.cn/api/anthropic/v1");

  const savedGeneric = serializeForSave({
    defaultModel: "zhipu/glm-4.7",
    thinkingLevel: "",
    providers: [
      {
        _key: "p-z",
        name: "zhipu",
        api: "anthropic-messages",
        baseUrl: "https://open.bigmodel.cn/api/anthropic",
        apiKey: " sk-zhipu ",
        models: [{ _key: "m-z", id: "glm-4.7", baseUrl: "https://open.bigmodel.cn/api/anthropic/v1/messages" }],
      },
    ],
  });
  assert.equal(savedGeneric.providers[0].baseUrl, "https://open.bigmodel.cn/api/anthropic/v1");
  assert.equal(savedGeneric.providers[0].models[0].baseUrl, "https://open.bigmodel.cn/api/anthropic/v1");
});
