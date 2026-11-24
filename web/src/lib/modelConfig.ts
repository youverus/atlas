import type { ModelConfig, ProviderConfig, SubagentModelConfig } from "../api";

export const DEEPSEEK_OPENAI_BASE_URL = "https://api.deepseek.com/v1";
export const DEEPSEEK_ANTHROPIC_BASE_URL = "https://api.deepseek.com/anthropic/v1";

export interface DraftModel {
  _key: string;
  id: string;
  name?: string;
  baseUrl?: string;
  contextWindow?: number;
  maxTokens?: number;
  reasoning?: boolean;
  thinkingLevels?: string[];
  input?: string[];
  userAgent?: string;
}

export interface DraftProvider extends Omit<ProviderConfig, "models"> {
  _key: string;
  models: DraftModel[];
}

export interface DraftConfig {
  configPath?: string;
  defaultModel: string;
  thinkingLevel: string;
  providers: DraftProvider[];
  subagents?: SubagentModelConfig;
}

export interface ProviderDiagnostic {
  level: "warning" | "info";
  message: string;
  suggestedBaseUrl?: string;
}

const DEFAULT_BASE_URL_PLACEHOLDERS: Record<string, string> = {
  "openai-completions": "留空默认 https://api.openai.com/v1（DeepSeek 请填 https://api.deepseek.com/v1）",
  "anthropic-messages": "留空默认 https://api.anthropic.com/v1；填写裸域名、/anthropic 或 /v1/messages 会自动规范化",
  "openai-responses": "留空则使用 https://api.openai.com/v1",
  "google-generative-ai": "留空则使用 Google 官方 Gemini 地址",
};

export function baseUrlPlaceholder(api: string, providerName = ""): string {
  const normalizedApi = api.trim();
  if (providerName.trim().toLowerCase() === "deepseek") {
    if (normalizedApi === "anthropic-messages") {
      return `DeepSeek Anthropic 兼容地址：${DEEPSEEK_ANTHROPIC_BASE_URL}`;
    }
    if (normalizedApi === "openai-completions") {
      return `DeepSeek 官方地址：${DEEPSEEK_OPENAI_BASE_URL}`;
    }
  }
  return DEFAULT_BASE_URL_PLACEHOLDERS[normalizedApi] ?? "留空则使用对应协议的官方默认地址";
}

function parseURL(raw: string): { origin: string; host: string; path: string; lowerPath: string } | null {
  const trimmed = raw.trim().replace(/\/+$/, "");
  if (!trimmed) return null;
  let parsed: URL;
  try {
    parsed = new URL(trimmed);
  } catch {
    return null;
  }
  const path = parsed.pathname.replace(/\/+$/, "");
  return {
    origin: parsed.origin,
    host: parsed.hostname.toLowerCase(),
    path,
    lowerPath: path.toLowerCase(),
  };
}

// Normalizes any anthropic-messages baseUrl (bare origin, Anthropic SDK root
// like /anthropic or /api/anthropic or /coding, or full /messages URL) to the
// versioned root expected before appending /messages.
export function normalizeAnthropicBaseUrl(raw: string): string {
  const trimmed = raw.trim().replace(/\/+$/, "");
  if (!trimmed) return "";
  const info = parseURL(trimmed);
  if (!info) return trimmed;

  let path = info.path;
  let lowerPath = info.lowerPath;
  if (lowerPath === "/messages" || lowerPath.endsWith("/messages")) {
    path = path.slice(0, path.length - "/messages".length).replace(/\/+$/, "");
    lowerPath = path.toLowerCase();
  }

  if (info.host === "api.deepseek.com") {
    if (lowerPath === "" || lowerPath === "/v1" || lowerPath === "/anthropic" || lowerPath === "/anthropic/v1") {
      return `${info.origin}/anthropic/v1`;
    }
  }

  if (lowerPath === "") {
    return `${info.origin}/v1`;
  }

  const slashIdx = lowerPath.lastIndexOf("/");
  const lastSeg = slashIdx >= 0 ? lowerPath.slice(slashIdx + 1) : lowerPath;
  if (/^v\d+/.test(lastSeg)) {
    return `${info.origin}${path}`;
  }

  if (lastSeg === "anthropic" || lastSeg === "coding" || lastSeg === "api") {
    return `${info.origin}${path}/v1`;
  }

  return `${info.origin}${path}`;
}

export function adaptBaseUrlForProtocol(currentBaseUrl: string, nextApi: string): string {
  if (!currentBaseUrl.trim()) return currentBaseUrl;
  if (nextApi === "anthropic-messages") {
    return normalizeAnthropicBaseUrl(currentBaseUrl);
  }
  const info = parseURL(currentBaseUrl);
  if (info && info.host === "api.deepseek.com" && nextApi === "openai-completions") {
    if (
      info.lowerPath === "" ||
      info.lowerPath === "/anthropic" ||
      info.lowerPath === "/anthropic/v1" ||
      info.lowerPath === "/v1"
    ) {
      return `${info.origin}/v1`;
    }
  }
  return currentBaseUrl;
}

export function nextUniqueProviderName(existing: readonly string[], preferred: string): string {
  const used = new Set(existing.map((name) => name.trim()).filter(Boolean));
  if (!used.has(preferred)) return preferred;
  let index = 2;
  while (used.has(`${preferred}-${index}`)) {
    index += 1;
  }
  return `${preferred}-${index}`;
}

export function createDeepSeekProviderPreset(
  existingProviders: readonly DraftProvider[],
  makeKey: (prefix: string) => string,
): DraftProvider {
  const name = nextUniqueProviderName(
    existingProviders.map((provider) => provider.name),
    "deepseek",
  );
  return {
    _key: makeKey("prov"),
    name,
    api: "openai-completions",
    baseUrl: DEEPSEEK_OPENAI_BASE_URL,
    apiKey: "",
    apiKeyEnv: "DEEPSEEK_API_KEY",
    userAgent: "",
    compat: {
      thinkingDialect: "deepseek",
      requireReasoningContent: true,
    },
    models: [
      {
        _key: makeKey("model"),
        id: "deepseek-flash",
        name: "DeepSeek Flash",
        baseUrl: "",
        contextWindow: 128000,
        maxTokens: 8192,
        reasoning: true,
        thinkingLevels: [],
        input: ["text"],
        userAgent: "",
      },
      {
        _key: makeKey("model"),
        id: "deepseek-v4-pro",
        name: "DeepSeek V4 Pro",
        baseUrl: "",
        contextWindow: 128000,
        maxTokens: 8192,
        reasoning: true,
        thinkingLevels: [],
        input: ["text"],
        userAgent: "",
      },
    ],
  };
}

export function serializeForSave(draft: DraftConfig): ModelConfig {
  return {
    configPath: draft.configPath,
    defaultModel: draft.defaultModel.trim(),
    thinkingLevel: draft.thinkingLevel.trim(),
    subagents: draft.subagents,
    providers: draft.providers.map((provider) => {
      const api = provider.api.trim();
      const rawBaseUrl = provider.baseUrl?.trim() || "";
      const baseUrl =
        api === "anthropic-messages" && rawBaseUrl ? normalizeAnthropicBaseUrl(rawBaseUrl) : rawBaseUrl;
      return {
        name: provider.name.trim(),
        api,
        baseUrl,
        apiKey: provider.apiKey?.trim() ?? "",
        apiKeyEnv: provider.apiKeyEnv?.trim() || "",
        userAgent: provider.userAgent?.trim() || "",
        compat: provider.compat,
        models: provider.models.map((model) => {
          const rawModelBaseUrl = model.baseUrl?.trim() || "";
          const modelBaseUrl =
            api === "anthropic-messages" && rawModelBaseUrl
              ? normalizeAnthropicBaseUrl(rawModelBaseUrl)
              : rawModelBaseUrl;
          return {
            id: model.id.trim(),
            name: model.name?.trim() || "",
            baseUrl: modelBaseUrl,
            contextWindow: Number.isFinite(model.contextWindow)
              ? Math.max(0, Math.trunc(model.contextWindow ?? 0))
              : 0,
            maxTokens: Number.isFinite(model.maxTokens)
              ? Math.max(0, Math.trunc(model.maxTokens ?? 0))
              : 0,
            reasoning: Boolean(model.reasoning),
            thinkingLevels: model.reasoning ? [...(model.thinkingLevels ?? [])] : [],
            input: [...(model.input ?? [])],
            userAgent: model.userAgent?.trim() || "",
          };
        }),
      };
    }),
  };
}

export function validateDraft(cfg: ModelConfig): string | null {
  if (cfg.providers.length === 0) {
    return "至少需要保留一个服务商和模型。";
  }
  const seenProviders = new Set<string>();
  let totalModels = 0;
  for (const provider of cfg.providers) {
    if (!provider.name) {
      return "服务商标识（Name）不能为空。";
    }
    if (provider.name.includes("/")) {
      return `服务商标识 "${provider.name}" 不能包含 "/"。`;
    }
    if (seenProviders.has(provider.name)) {
      return `服务商标识 "${provider.name}" 重复。`;
    }
    seenProviders.add(provider.name);
    if (!provider.api) {
      return `服务商 "${provider.name}" 未选择协议类型（API）。`;
    }
    if (provider.models.length === 0) {
      return `服务商 "${provider.name}" 至少需要一个模型。`;
    }
    const seenModels = new Set<string>();
    for (const model of provider.models) {
      if (!model.id) {
        return `服务商 "${provider.name}" 存在模型 ID 为空的条目。`;
      }
      if (seenModels.has(model.id)) {
        return `服务商 "${provider.name}" 下模型 ID "${model.id}" 重复。`;
      }
      seenModels.add(model.id);
      totalModels += 1;
    }
  }
  if (totalModels > 1 && !cfg.defaultModel) {
    return "配置了多个模型时，请指定一个默认模型。";
  }
  return null;
}

export function diagnoseProviderConfig(provider: ProviderConfig): ProviderDiagnostic[] {
  const diagnostics: ProviderDiagnostic[] = [];
  const api = provider.api.trim();
  const baseUrl = (provider.baseUrl ?? "").trim();
  const nameLower = provider.name.trim().toLowerCase();
  const hasDeepSeekModel = provider.models.some((model) =>
    model.id.trim().toLowerCase().startsWith("deepseek-"),
  );
  const urlInfo = parseURL(baseUrl);

  if (api === "anthropic-messages" && baseUrl) {
    const normalized = normalizeAnthropicBaseUrl(baseUrl);
    if (normalized && normalized !== baseUrl.replace(/\/+$/, "")) {
      const isDeepSeekMismatch =
        urlInfo?.host === "api.deepseek.com" && (urlInfo.lowerPath === "/v1" || urlInfo.lowerPath === "");
      diagnostics.push({
        level: isDeepSeekMismatch ? "warning" : "info",
        message: `该 anthropic-messages 地址将规范化为 ${normalized}（实际请求 ${normalized}/messages），保存时会自动写入规范地址。`,
        suggestedBaseUrl: normalized,
      });
    }
  } else if (
    urlInfo?.host === "api.deepseek.com" &&
    api === "openai-completions" &&
    (urlInfo.lowerPath === "/anthropic" || urlInfo.lowerPath === "/anthropic/v1")
  ) {
    diagnostics.push({
      level: "warning",
      message: "openai-completions 协议下 DeepSeek 官方地址应为 https://api.deepseek.com/v1。",
      suggestedBaseUrl: `${urlInfo.origin}/v1`,
    });
  } else if (!baseUrl && (nameLower === "deepseek" || hasDeepSeekModel)) {
    const suggested =
      api === "anthropic-messages" ? DEEPSEEK_ANTHROPIC_BASE_URL : DEEPSEEK_OPENAI_BASE_URL;
    if (api === "anthropic-messages" || api === "openai-completions") {
      diagnostics.push({
        level: "warning",
        message: `DeepSeek 服务商未填写 Base URL（留空会请求 OpenAI/Anthropic 官方地址而非 DeepSeek），官方地址应为 ${suggested}。`,
        suggestedBaseUrl: suggested,
      });
    }
  }

  const key = (provider.apiKey ?? "").trim();
  const envName = (provider.apiKeyEnv ?? "").trim();
  if (provider.apiKeyEnvSet && envName) {
    if (key) {
      diagnostics.push({
        level: "warning",
        message: `系统环境变量 ${envName} 当前已有值且优先级高于上方 API Key；若需改用输入框中的密钥，请清空 apiKeyEnv 或更新环境变量。`,
      });
    } else {
      diagnostics.push({
        level: "info",
        message: `已检测到环境变量 ${envName} 生效，将优先使用该环境变量中的密钥。`,
      });
    }
  } else if (!key && envName) {
    diagnostics.push({
      level: "warning",
      message: `未填写 API Key，且环境变量 ${envName} 当前未设置（为空）；直接发送请求会报 401 认证失败。`,
    });
  } else if (!key && !envName) {
    diagnostics.push({
      level: "info",
      message: "未配置 API Key 或环境变量（仅免鉴权本地网关可留空）。",
    });
  }

  return diagnostics;
}
