import { forwardRef, useEffect, useImperativeHandle, useMemo, useState } from "react";
import type { CompatConfig, ModelConfig } from "../api";
import {
  adaptBaseUrlForProtocol,
  baseUrlPlaceholder,
  createDeepSeekProviderPreset,
  diagnoseProviderConfig,
  serializeForSave,
  validateDraft,
  type DraftConfig,
  type DraftModel,
  type DraftProvider,
} from "../lib/modelConfig";

export interface ModelConfigEditorHandle {
  isDirty: () => boolean;
  saveIfDirty: () => Promise<boolean>;
}

const API_PROTOCOLS = [
  { value: "openai-completions", label: "openai-completions (OpenAI Chat 兼容)" },
  { value: "anthropic-messages", label: "anthropic-messages (Anthropic)" },
  { value: "openai-responses", label: "openai-responses (OpenAI Responses)" },
  { value: "google-generative-ai", label: "google-generative-ai (Google Gemini)" },
];

const THINKING_LEVEL_OPTIONS = ["off", "low", "medium", "high", "xhigh", "max", "ultra"];
const MODEL_GEAR_OPTIONS = ["low", "medium", "high", "xhigh", "max", "ultra"];
const SUBAGENT_MODEL_FIELDS = [
  { key: "defaultModel", label: "子代理默认模型", inherit: "继承主会话模型" },
  { key: "explorer", label: "@explorer 模型", inherit: "继承子代理默认模型" },
  { key: "verifier", label: "@verifier 模型", inherit: "继承子代理默认模型" },
] as const;
const INPUT_MODALITIES = [
  { value: "text", label: "文本 (text)" },
  { value: "image", label: "图片 (image)" },
];

let keyCounter = 0;
function nextKey(prefix: string): string {
  keyCounter += 1;
  return `${prefix}-${keyCounter}`;
}

function toDraft(cfg: ModelConfig): DraftConfig {
  return {
    configPath: cfg.configPath,
    defaultModel: cfg.defaultModel ?? "",
    thinkingLevel: cfg.thinkingLevel ?? "",
    subagents: cfg.subagents ? { ...cfg.subagents } : undefined,
    providers: (cfg.providers ?? []).map((provider) => ({
      ...provider,
      _key: nextKey("prov"),
      baseUrl: provider.baseUrl ?? "",
      apiKey: provider.apiKey ?? "",
      apiKeyEnv: provider.apiKeyEnv ?? "",
      userAgent: provider.userAgent ?? "",
      compat: provider.compat ? { ...provider.compat } : undefined,
      models: (provider.models ?? []).map((model) => ({
        ...model,
        _key: nextKey("model"),
        id: model.id ?? "",
        name: model.name ?? "",
        baseUrl: model.baseUrl ?? "",
        contextWindow: model.contextWindow ?? 0,
        maxTokens: model.maxTokens ?? 0,
        reasoning: Boolean(model.reasoning),
        thinkingLevels: [...(model.thinkingLevels ?? [])],
        input: [...(model.input ?? [])],
        userAgent: model.userAgent ?? "",
      })),
    })),
  };
}

function firstValidSelection(providers: DraftProvider[]): string {
  for (const provider of providers) {
    const provName = provider.name.trim();
    if (!provName) continue;
    for (const model of provider.models) {
      const id = model.id.trim();
      if (id) return `${provName}/${id}`;
    }
  }
  return "";
}

function modelRef(providerName: string, modelId: string): string {
  const p = providerName.trim();
  const m = modelId.trim();
  return p && m ? `${p}/${m}` : "";
}

function hasMatches(draft: DraftConfig, target: string): boolean {
  if (!target) return false;
  for (const provider of draft.providers) {
    const provName = provider.name.trim();
    if (!provName) continue;
    for (const model of provider.models) {
      const id = model.id.trim();
      if (!id) continue;
      if (`${provName}/${id}` === target || id === target) return true;
    }
  }
  return false;
}

export const ModelConfigEditor = forwardRef<
  ModelConfigEditorHandle,
  {
    initial: ModelConfig;
    busy: boolean;
    onReload: () => Promise<void>;
    onSave: (next: ModelConfig) => Promise<void>;
    onDirtyChange?: (dirty: boolean) => void;
  }
>(function ModelConfigEditor({ initial, busy, onReload, onSave, onDirtyChange }, ref) {
  const [draft, setDraft] = useState<DraftConfig>(() => toDraft(initial));
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [localError, setLocalError] = useState("");
  const [savedNotice, setSavedNotice] = useState("");
  const [expandedProviders, setExpandedProviders] = useState<Record<string, boolean>>(() => {
    const next: Record<string, boolean> = {};
    const first = toDraft(initial).providers[0];
    if (first && initial.providers.length === 1) {
      next[first._key] = true;
    }
    return next;
  });
  const [revealedKeys, setRevealedKeys] = useState<Record<string, boolean>>({});

  // Keep draft synced when parent explicitly reloads and there are no unsaved edits,
  // or provide explicit reset button when dirty.
  const [syncedSource, setSyncedSource] = useState<ModelConfig>(initial);
  if (syncedSource !== initial) {
    setSyncedSource(initial);
    const next = toDraft(initial);
    setDraft(next);
    setDirty(false);
    setExpandedProviders((prev) => {
      if (Object.keys(prev).length > 0) {
        const mapped: Record<string, boolean> = {};
        for (const p of next.providers) {
          mapped[p._key] = next.providers.length <= 2;
        }
        return mapped;
      }
      return next.providers.length === 1 ? { [next.providers[0]._key]: true } : {};
    });
  }

  const allModelOptions = useMemo(() => {
    const list: { value: string; label: string }[] = [];
    for (const provider of draft.providers) {
      const provName = provider.name.trim();
      if (!provName) continue;
      for (const model of provider.models) {
        const id = model.id.trim();
        if (!id) continue;
        const value = `${provName}/${id}`;
        const display = model.name?.trim();
        list.push({
          value,
          label: display && display !== id ? `${display} (${value})` : value,
        });
      }
    }
    return list;
  }, [draft.providers]);

  const mutate = (updater: (prev: DraftConfig) => DraftConfig) => {
    setLocalError("");
    setSavedNotice("");
    setDirty(true);
    setDraft((prev) => updater(prev));
  };

  const addProvider = () => {
    const existing = new Set(draft.providers.map((p) => p.name.trim()));
    let index = draft.providers.length + 1;
    let name = `provider-${index}`;
    while (existing.has(name)) {
      index += 1;
      name = `provider-${index}`;
    }
    const provKey = nextKey("prov");
    const newProvider: DraftProvider = {
      _key: provKey,
      name,
      api: "openai-completions",
      baseUrl: "",
      apiKey: "",
      apiKeyEnv: "",
      userAgent: "",
      models: [
        {
          _key: nextKey("model"),
          id: "",
          name: "",
          baseUrl: "",
          contextWindow: 128000,
          maxTokens: 8192,
          reasoning: false,
          thinkingLevels: [],
          input: ["text"],
          userAgent: "",
        },
      ],
    };
    mutate((prev) => ({
      ...prev,
      providers: [...prev.providers, newProvider],
    }));
    setExpandedProviders((prev) => ({ ...prev, [provKey]: true }));
  };

  const addDeepSeekPreset = () => {
    const preset = createDeepSeekProviderPreset(draft.providers, nextKey);
    mutate((prev) => ({
      ...prev,
      defaultModel: prev.defaultModel.trim() || `${preset.name}/deepseek-flash`,
      providers: [...prev.providers, preset],
    }));
    setExpandedProviders((prev) => ({ ...prev, [preset._key]: true }));
  };

  const removeProvider = (provKey: string) => {
    mutate((prev) => {
      const remaining = prev.providers.filter((p) => p._key !== provKey);
      const nextDefault = hasMatches({ ...prev, providers: remaining }, prev.defaultModel)
        ? prev.defaultModel
        : firstValidSelection(remaining);
      return {
        ...prev,
        defaultModel: nextDefault,
        providers: remaining,
      };
    });
  };

  const updateProvider = (provKey: string, patch: Partial<DraftProvider>) => {
    mutate((prev) => {
      const target = prev.providers.find((p) => p._key === provKey);
      const oldName = target?.name.trim() ?? "";
      const nextPatch: Partial<DraftProvider> = { ...patch };
      if (patch.api !== undefined && target && patch.baseUrl === undefined) {
        const adapted = adaptBaseUrlForProtocol(target.baseUrl ?? "", patch.api);
        if (adapted !== (target.baseUrl ?? "")) {
          nextPatch.baseUrl = adapted;
        }
      }
      if (patch.apiKeyEnv !== undefined && target && patch.apiKeyEnv.trim() !== (target.apiKeyEnv ?? "").trim()) {
        nextPatch.apiKeyEnvSet = undefined;
      }
      const providers = prev.providers.map((p) => (p._key === provKey ? { ...p, ...nextPatch } : p));
      let defaultModel = prev.defaultModel;
      if (patch.name !== undefined) {
        const nextName = patch.name.trim();
        if (oldName && nextName && defaultModel.startsWith(`${oldName}/`)) {
          defaultModel = `${nextName}/${defaultModel.slice(oldName.length + 1)}`;
        }
      }
      return { ...prev, defaultModel, providers };
    });
  };

  const updateCompat = (provKey: string, patch: Partial<CompatConfig>) => {
    mutate((prev) => ({
      ...prev,
      providers: prev.providers.map((p) =>
        p._key === provKey ? { ...p, compat: { ...(p.compat ?? {}), ...patch } } : p,
      ),
    }));
  };

  const addModel = (provKey: string) => {
    mutate((prev) => {
      const providers = prev.providers.map((p) => {
        if (p._key !== provKey) return p;
        return {
          ...p,
          models: [
            ...p.models,
            {
              _key: nextKey("model"),
              id: "",
              name: "",
              baseUrl: "",
              contextWindow: 128000,
              maxTokens: 8192,
              reasoning: false,
              thinkingLevels: [],
              input: ["text"],
              userAgent: "",
            },
          ],
        };
      });
      return { ...prev, providers };
    });
    setExpandedProviders((prev) => ({ ...prev, [provKey]: true }));
  };

  const updateModel = (provKey: string, modelKey: string, patch: Partial<DraftModel>) => {
    mutate((prev) => {
      const provider = prev.providers.find((p) => p._key === provKey);
      const oldModel = provider?.models.find((m) => m._key === modelKey);
      const provName = provider?.name.trim() ?? "";
      const oldRef = provName && oldModel?.id.trim() ? `${provName}/${oldModel.id.trim()}` : "";
      let defaultModel = prev.defaultModel;
      if (patch.id !== undefined && oldRef && defaultModel === oldRef) {
        const nextID = patch.id.trim();
        defaultModel = nextID ? `${provName}/${nextID}` : "";
      }
      const providers = prev.providers.map((p) =>
        p._key !== provKey
          ? p
          : {
              ...p,
              models: p.models.map((m) => (m._key === modelKey ? { ...m, ...patch } : m)),
            },
      );
      if (!defaultModel) {
        defaultModel = firstValidSelection(providers);
      }
      return { ...prev, defaultModel, providers };
    });
  };

  const removeModel = (provKey: string, modelKey: string) => {
    mutate((prev) => {
      const providers = prev.providers.map((p) =>
        p._key !== provKey ? p : { ...p, models: p.models.filter((m) => m._key !== modelKey) },
      );
      const nextDefault = hasMatches({ ...prev, providers }, prev.defaultModel)
        ? prev.defaultModel
        : firstValidSelection(providers);
      return {
        ...prev,
        defaultModel: nextDefault,
        providers,
      };
    });
  };

  const toggleInputModality = (provKey: string, model: DraftModel, modality: string) => {
    const current = model.input ?? [];
    const next = current.includes(modality)
      ? current.filter((item) => item !== modality)
      : [...current, modality];
    updateModel(provKey, model._key, { input: next });
  };

  const toggleModelGear = (provKey: string, model: DraftModel, gear: string) => {
    const current = model.thinkingLevels ?? [];
    const exists = current.includes(gear);
    const unordered = exists ? current.filter((item) => item !== gear) : [...current, gear];
    const next = MODEL_GEAR_OPTIONS.filter((item) => unordered.includes(item));
    updateModel(provKey, model._key, { thinkingLevels: next });
  };

  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [dirty, onDirtyChange]);

  const handleSave = async (): Promise<boolean> => {
    const payload = serializeForSave(draft);
    const problem = validateDraft(payload);
    if (problem) {
      setLocalError(problem);
      setSavedNotice("");
      return false;
    }
    setSaving(true);
    setLocalError("");
    setSavedNotice("");
    try {
      await onSave(payload);
      setDirty(false);
      setSavedNotice("已覆盖保存配置文件并热加载生效");
      return true;
    } catch (cause) {
      setLocalError(cause instanceof Error ? cause.message : String(cause));
      return false;
    } finally {
      setSaving(false);
    }
  };

  useImperativeHandle(
    ref,
    () => ({
      isDirty: () => dirty,
      saveIfDirty: () => (dirty ? handleSave() : Promise.resolve(true)),
    }),
  );

  const handleReload = async () => {
    setLocalError("");
    setSavedNotice("");
    await onReload();
    setDirty(false);
  };

  const disabled = busy || saving;

  return (
    <div className="model-editor">
      {draft.configPath ? (
        <div className="model-editor__path" title={draft.configPath}>
          配置文件：{draft.configPath}
        </div>
      ) : null}

      {localError ? <div className="notice notice--error">{localError}</div> : null}
      {savedNotice ? <div className="model-editor__saved">{savedNotice}</div> : null}

      <div className="model-editor__globals">
        <div className="model-editor__field">
          <label className="model-editor__label" htmlFor="model-editor-default">
            默认模型 (defaultModel)
          </label>
          <select
            id="model-editor-default"
            className="select"
            disabled={disabled || allModelOptions.length === 0}
            value={allModelOptions.some((o) => o.value === draft.defaultModel) ? draft.defaultModel : ""}
            onChange={(event) => mutate((prev) => ({ ...prev, defaultModel: event.target.value }))}
          >
            {allModelOptions.length === 0 ? (
              <option value="">请先添加模型</option>
            ) : (
              <>
                {!allModelOptions.some((o) => o.value === draft.defaultModel) ? (
                  <option value="">{draft.defaultModel || "请选择默认模型"}</option>
                ) : null}
                {allModelOptions.map((option) => (
                  <option key={option.value} value={option.value}>
                    {option.label}
                  </option>
                ))}
              </>
            )}
          </select>
        </div>

        <div className="model-editor__field">
          <label className="model-editor__label" htmlFor="model-editor-thinking">
            默认推理强度 (thinkingLevel)
          </label>
          <select
            id="model-editor-thinking"
            className="select"
            disabled={disabled}
            value={draft.thinkingLevel}
            onChange={(event) => mutate((prev) => ({ ...prev, thinkingLevel: event.target.value }))}
          >
            <option value="">自动（由模型决定）</option>
            {THINKING_LEVEL_OPTIONS.map((level) => (
              <option key={level} value={level}>
                {level}
              </option>
            ))}
          </select>
        </div>
      </div>

      {draft.subagents ? (
        <div className="model-editor__globals">
          {SUBAGENT_MODEL_FIELDS.map(({ key, label, inherit }) => {
            const value = draft.subagents?.[key] ?? "";
            const id = `model-editor-subagent-${key}`;
            return (
              <div className="model-editor__field" key={key}>
                <label className="model-editor__label" htmlFor={id}>{label}</label>
                <select
                  id={id}
                  className="select"
                  disabled={disabled}
                  value={value}
                  onChange={(event) => {
                    const next = event.currentTarget.value;
                    mutate((prev) => ({ ...prev, subagents: { ...prev.subagents, [key]: next } }));
                  }}
                >
                  <option value="">{inherit}</option>
                  {value && !allModelOptions.some((option) => option.value === value) ? (
                    <option value={value}>{value}</option>
                  ) : null}
                  {allModelOptions.map((option) => (
                    <option key={option.value} value={option.value}>{option.label}</option>
                  ))}
                </select>
              </div>
            );
          })}
        </div>
      ) : null}

      <div className="model-editor__toolbar">
        <span className="model-editor__section-title">
          服务商与模型（{draft.providers.length} 个服务商 · {allModelOptions.length} 个模型）
        </span>
        <div className="model-editor__toolbar-actions">
          <button type="button" className="button" disabled={disabled} onClick={addDeepSeekPreset}>
            + 添加 DeepSeek 官方
          </button>
          <button type="button" className="button" disabled={disabled} onClick={addProvider}>
            + 添加服务商
          </button>
        </div>
      </div>

      <div className="model-editor__providers">
        {draft.providers.map((provider) => {
          const isOpen = expandedProviders[provider._key] ?? false;
          const keyVisible = Boolean(revealedKeys[provider._key]);
          const compat = provider.compat ?? {};
          const diagnostics = diagnoseProviderConfig(provider);
          return (
            <section className="model-editor__card" key={provider._key}>
              <div className="model-editor__card-head">
                <button
                  type="button"
                  className="model-editor__card-toggle"
                  aria-expanded={isOpen}
                  onClick={() =>
                    setExpandedProviders((prev) => ({
                      ...prev,
                      [provider._key]: !isOpen,
                    }))
                  }
                >
                  <span className="model-editor__card-name">
                    {provider.name.trim() || "未命名服务商"}
                  </span>
                  <span className="mcp-badge">{provider.api}</span>
                  <span className="model-editor__card-count">{provider.models.length} 个模型</span>
                  <span className="mcp-card__chevron" aria-hidden="true">
                    {isOpen ? "▾" : "▸"}
                  </span>
                </button>
                <button
                  type="button"
                  className="model-editor__danger-btn"
                  disabled={disabled}
                  onClick={() => removeProvider(provider._key)}
                >
                  删除服务商
                </button>
              </div>

              {isOpen ? (
                <div className="model-editor__card-body">
                  <div className="model-editor__grid">
                    <div className="model-editor__field">
                      <label className="model-editor__label">服务商标识 (Name)*</label>
                      <input
                        className="field__input"
                        type="text"
                        disabled={disabled}
                        placeholder="如 deepseek / openai / local"
                        value={provider.name}
                        onChange={(event) => updateProvider(provider._key, { name: event.target.value })}
                      />
                    </div>

                    <div className="model-editor__field">
                      <label className="model-editor__label">协议格式 (API)*</label>
                      <select
                        className="select"
                        disabled={disabled}
                        value={provider.api}
                        onChange={(event) => updateProvider(provider._key, { api: event.target.value })}
                      >
                        {!API_PROTOCOLS.some((p) => p.value === provider.api) && provider.api ? (
                          <option value={provider.api}>{provider.api}</option>
                        ) : null}
                        {API_PROTOCOLS.map((proto) => (
                          <option key={proto.value} value={proto.value}>
                            {proto.label}
                          </option>
                        ))}
                      </select>
                    </div>

                    <div className="model-editor__field model-editor__field--full">
                      <label className="model-editor__label">Base URL</label>
                      <input
                        className="field__input"
                        type="text"
                        disabled={disabled}
                        placeholder={baseUrlPlaceholder(provider.api, provider.name)}
                        value={provider.baseUrl ?? ""}
                        onChange={(event) => updateProvider(provider._key, { baseUrl: event.target.value })}
                      />
                    </div>

                    <div className="model-editor__field">
                      <label className="model-editor__label">API Key</label>
                      <div className="model-editor__secret-row">
                        <input
                          className="field__input"
                          type={keyVisible ? "text" : "password"}
                          disabled={disabled}
                          placeholder="直接填写密钥（本地 0600 保存）"
                          value={provider.apiKey ?? ""}
                          onChange={(event) => updateProvider(provider._key, { apiKey: event.target.value })}
                        />
                        <button
                          type="button"
                          className="button model-editor__mini-btn"
                          onClick={() =>
                            setRevealedKeys((prev) => ({
                              ...prev,
                              [provider._key]: !keyVisible,
                            }))
                          }
                        >
                          {keyVisible ? "隐藏" : "显示"}
                        </button>
                      </div>
                    </div>

                    <div className="model-editor__field">
                      <label className="model-editor__label">API Key 环境变量 (apiKeyEnv)</label>
                      <input
                        className="field__input"
                        type="text"
                        disabled={disabled}
                        placeholder="如 DEEPSEEK_API_KEY（优先级高于字面量）"
                        value={provider.apiKeyEnv ?? ""}
                        onChange={(event) => updateProvider(provider._key, { apiKeyEnv: event.target.value })}
                      />
                    </div>

                    {diagnostics.length > 0 ? (
                      <div className="model-editor__field model-editor__field--full model-editor__hints">
                        {diagnostics.map((diag, idx) => (
                          <div
                            key={`${diag.level}-${idx}`}
                            className={`model-editor__hint model-editor__hint--${diag.level}`}
                          >
                            <span>{diag.message}</span>
                            {diag.suggestedBaseUrl ? (
                              <button
                                type="button"
                                className="button model-editor__mini-btn"
                                disabled={disabled}
                                onClick={() =>
                                  updateProvider(provider._key, { baseUrl: diag.suggestedBaseUrl })
                                }
                              >
                                一键修正 Base URL
                              </button>
                            ) : null}
                          </div>
                        ))}
                      </div>
                    ) : null}

                    <div className="model-editor__field model-editor__field--full">
                      <label className="model-editor__label">User-Agent (可选)</label>
                      <input
                        className="field__input"
                        type="text"
                        disabled={disabled}
                        placeholder="留空使用默认"
                        value={provider.userAgent ?? ""}
                        onChange={(event) => updateProvider(provider._key, { userAgent: event.target.value })}
                      />
                    </div>
                  </div>

                  <details className="model-editor__compat">
                    <summary className="model-editor__compat-summary">高级协议兼容选项 (compat)</summary>
                    <div className="model-editor__compat-body">
                      <div className="model-editor__grid">
                        <div className="model-editor__field">
                          <label className="model-editor__label">maxTokensField</label>
                          <select
                            className="select"
                            disabled={disabled}
                            value={compat.maxTokensField ?? ""}
                            onChange={(event) =>
                              updateCompat(provider._key, { maxTokensField: event.target.value })
                            }
                          >
                            <option value="">自动判断</option>
                            <option value="max_tokens">max_tokens</option>
                            <option value="max_completion_tokens">max_completion_tokens</option>
                          </select>
                        </div>
                        <div className="model-editor__field">
                          <label className="model-editor__label">thinkingDialect</label>
                          <select
                            className="select"
                            disabled={disabled}
                            value={compat.thinkingDialect ?? ""}
                            onChange={(event) =>
                              updateCompat(provider._key, { thinkingDialect: event.target.value })
                            }
                          >
                            <option value="">默认</option>
                            <option value="deepseek">deepseek</option>
                            <option value="level">level</option>
                            <option value="budget">budget</option>
                          </select>
                        </div>
                      </div>
                      <div className="model-editor__checks">
                        <label className="model-editor__check">
                          <input
                            type="checkbox"
                            disabled={disabled}
                            checked={Boolean(compat.requireReasoningContent)}
                            onChange={(event) =>
                              updateCompat(provider._key, {
                                requireReasoningContent: event.target.checked,
                              })
                            }
                          />
                          <span>requireReasoningContent</span>
                        </label>
                        <label className="model-editor__check">
                          <input
                            type="checkbox"
                            disabled={disabled}
                            checked={Boolean(compat.disableStreamOptions)}
                            onChange={(event) =>
                              updateCompat(provider._key, {
                                disableStreamOptions: event.target.checked,
                              })
                            }
                          />
                          <span>disableStreamOptions</span>
                        </label>
                        <label className="model-editor__check">
                          <input
                            type="checkbox"
                            disabled={disabled}
                            checked={Boolean(compat.disableReasoningEffort)}
                            onChange={(event) =>
                              updateCompat(provider._key, {
                                disableReasoningEffort: event.target.checked,
                              })
                            }
                          />
                          <span>disableReasoningEffort</span>
                        </label>
                        <label className="model-editor__check">
                          <input
                            type="checkbox"
                            disabled={disabled}
                            checked={Boolean(compat.adaptiveThinking)}
                            onChange={(event) =>
                              updateCompat(provider._key, {
                                adaptiveThinking: event.target.checked,
                              })
                            }
                          />
                          <span>adaptiveThinking</span>
                        </label>
                        <label className="model-editor__check">
                          <input
                            type="checkbox"
                            disabled={disabled}
                            checked={Boolean(compat.sendStore)}
                            onChange={(event) =>
                              updateCompat(provider._key, {
                                sendStore: event.target.checked,
                              })
                            }
                          />
                          <span>sendStore</span>
                        </label>
                      </div>
                    </div>
                  </details>

                  <div className="model-editor__models-head">
                    <span className="model-editor__label">模型列表</span>
                    <button
                      type="button"
                      className="button model-editor__mini-btn"
                      disabled={disabled}
                      onClick={() => addModel(provider._key)}
                    >
                      + 添加模型
                    </button>
                  </div>

                  <div className="model-editor__models">
                    {provider.models.map((model) => {
                      const ref = modelRef(provider.name, model.id);
                      const isDefault = Boolean(ref) && draft.defaultModel.trim() === ref;
                      return (
                        <div className="model-editor__model" key={model._key}>
                          <div className="model-editor__model-head">
                            <span className="model-editor__model-title">
                              {model.id.trim() || "新模型"}
                              {model.name?.trim() && model.name.trim() !== model.id.trim()
                                ? ` · ${model.name.trim()}`
                                : ""}
                            </span>
                            <div className="model-editor__model-actions">
                              {ref ? (
                                isDefault ? (
                                  <span className="mcp-badge" data-tone="ok">
                                    默认模型
                                  </span>
                                ) : (
                                  <button
                                    type="button"
                                    className="model-editor__link-btn"
                                    disabled={disabled}
                                    onClick={() =>
                                      mutate((prev) => ({
                                        ...prev,
                                        defaultModel: ref,
                                      }))
                                    }
                                  >
                                    设为默认
                                  </button>
                                )
                              ) : null}
                              <button
                                type="button"
                                className="model-editor__danger-btn"
                                disabled={disabled}
                                onClick={() => removeModel(provider._key, model._key)}
                              >
                                删除模型
                              </button>
                            </div>
                          </div>

                          <div className="model-editor__grid">
                            <div className="model-editor__field">
                              <label className="model-editor__label">模型 ID (id)*</label>
                              <input
                                className="field__input"
                                type="text"
                                disabled={disabled}
                                placeholder="如 deepseek-chat"
                                value={model.id}
                                onChange={(event) =>
                                  updateModel(provider._key, model._key, { id: event.target.value })
                                }
                              />
                            </div>

                            <div className="model-editor__field">
                              <label className="model-editor__label">显示名称 (name)</label>
                              <input
                                className="field__input"
                                type="text"
                                disabled={disabled}
                                placeholder="留空则显示 ID"
                                value={model.name ?? ""}
                                onChange={(event) =>
                                  updateModel(provider._key, model._key, { name: event.target.value })
                                }
                              />
                            </div>

                            <div className="model-editor__field">
                              <label className="model-editor__label">上下文窗口 (contextWindow)</label>
                              <input
                                className="field__input"
                                type="number"
                                min={0}
                                step={1000}
                                disabled={disabled}
                                value={model.contextWindow ?? 0}
                                onChange={(event) =>
                                  updateModel(provider._key, model._key, {
                                    contextWindow: Number(event.target.value) || 0,
                                  })
                                }
                              />
                            </div>

                            <div className="model-editor__field">
                              <label className="model-editor__label">最大输出 (maxTokens)</label>
                              <input
                                className="field__input"
                                type="number"
                                min={0}
                                step={256}
                                disabled={disabled}
                                value={model.maxTokens ?? 0}
                                onChange={(event) =>
                                  updateModel(provider._key, model._key, {
                                    maxTokens: Number(event.target.value) || 0,
                                  })
                                }
                              />
                            </div>

                            <div className="model-editor__field model-editor__field--full">
                              <label className="model-editor__label">独立 Base URL (可选)</label>
                              <input
                                className="field__input"
                                type="text"
                                disabled={disabled}
                                placeholder="留空则继承服务商 Base URL"
                                value={model.baseUrl ?? ""}
                                onChange={(event) =>
                                  updateModel(provider._key, model._key, { baseUrl: event.target.value })
                                }
                              />
                            </div>
                          </div>

                          <div className="model-editor__caps">
                            <label className="model-editor__check">
                              <input
                                type="checkbox"
                                disabled={disabled}
                                checked={Boolean(model.reasoning)}
                                onChange={(event) =>
                                  updateModel(provider._key, model._key, {
                                    reasoning: event.target.checked,
                                  })
                                }
                              />
                              <span>支持推理 (reasoning)</span>
                            </label>

                            <div className="model-editor__modalities">
                              {INPUT_MODALITIES.map((mod) => (
                                <label className="model-editor__check" key={mod.value}>
                                  <input
                                    type="checkbox"
                                    disabled={disabled}
                                    checked={(model.input ?? []).includes(mod.value)}
                                    onChange={() => toggleInputModality(provider._key, model, mod.value)}
                                  />
                                  <span>{mod.label}</span>
                                </label>
                              ))}
                            </div>
                          </div>

                          {model.reasoning ? (
                            <div className="model-editor__gears">
                              <span className="model-editor__label">
                                可用推理档位（不勾选则支持全部）：
                              </span>
                              <div className="model-editor__gear-pills">
                                {MODEL_GEAR_OPTIONS.map((gear) => {
                                  const active = (model.thinkingLevels ?? []).includes(gear);
                                  return (
                                    <button
                                      key={gear}
                                      type="button"
                                      className="model-editor__gear-pill"
                                      aria-pressed={active}
                                      disabled={disabled}
                                      onClick={() => toggleModelGear(provider._key, model, gear)}
                                    >
                                      {gear}
                                    </button>
                                  );
                                })}
                              </div>
                            </div>
                          ) : null}
                        </div>
                      );
                    })}
                  </div>
                </div>
              ) : null}
            </section>
          );
        })}
      </div>

      <div className="model-editor__foot">
        <div className="sheet__actions">
          <button type="button" className="button" disabled={disabled} onClick={() => void handleReload()}>
            {dirty ? "放弃修改" : "重新读取"}
          </button>
          <button
            type="button"
            className="button button--primary"
            disabled={disabled}
            onClick={() => void handleSave()}
          >
            {saving ? "保存中…" : "保存并覆盖配置文件"}
          </button>
        </div>
      </div>
    </div>
  );
});
