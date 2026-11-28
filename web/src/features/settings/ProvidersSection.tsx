import { Plus, Trash2 } from "lucide-react";
import type {
  ConfigView,
  DecisionModelConfig,
  DecisionProviderConfig,
  ProviderConfig,
} from "@/client/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";

const LLM_PRESETS: Array<{ label: string; build: (n: number) => ProviderConfig }> = [
  {
    label: "OpenAI",
    build: () => ({
      id: "openai",
      type: "openai-compatible",
      kind: "openai",
      prefix: "openai",
      enabled: true,
      base_url: "https://api.openai.com/v1",
      api_key_env: "OPENAI_API_KEY",
    }),
  },
  {
    label: "OpenRouter",
    build: () => ({
      id: "openrouter",
      type: "openrouter",
      prefix: "openrouter",
      enabled: true,
      base_url: "https://openrouter.ai/api/v1",
      api_key_env: "OPENROUTER_API_KEY",
    }),
  },
  {
    label: "DeepInfra",
    build: () => ({
      id: "deepinfra",
      type: "openai-compatible",
      kind: "openai",
      prefix: "deepinfra",
      enabled: true,
      base_url: "https://api.deepinfra.com/v1/openai",
      api_key_env: "DEEPINFRA_API_KEY",
    }),
  },
  {
    label: "Google AI Studio",
    build: () => ({
      id: "google",
      type: "openai-compatible",
      kind: "generic",
      prefix: "google",
      enabled: true,
      base_url: "https://generativelanguage.googleapis.com/v1beta/openai",
      api_key_env: "GEMINI_API_KEY",
    }),
  },
  {
    label: "Anthropic",
    build: () => ({
      id: "anthropic",
      type: "anthropic",
      prefix: "anthropic",
      enabled: true,
      base_url: "https://api.anthropic.com",
      api_key_env: "ANTHROPIC_API_KEY",
    }),
  },
  {
    label: "xAI / Grok",
    build: () => ({
      id: "grok",
      type: "grok",
      prefix: "grok",
      enabled: true,
      base_url: "https://api.x.ai/v1",
      api_key_env: "XAI_API_KEY",
    }),
  },
  {
    label: "Codex App Server",
    build: () => ({
      id: "codex",
      type: "codex",
      prefix: "codex",
      enabled: true,
      codex: { command: "codex" },
    }),
  },
  {
    label: "Custom",
    build: (n) => ({
      id: `provider-${n}`,
      type: "openai-compatible",
      prefix: `p${n}`,
      enabled: true,
      base_url: "http://127.0.0.1:11434/v1",
    }),
  },
];

function uniqueId(base: string, existing: string[]): string {
  if (!existing.includes(base)) return base;
  let i = 2;
  while (existing.includes(`${base}-${i}`)) i++;
  return `${base}-${i}`;
}

function parseIntOpt(raw: string): number | undefined {
  const trimmed = raw.trim();
  if (!trimmed) return undefined;
  const parsed = Number(trimmed);
  return Number.isFinite(parsed) ? Math.trunc(parsed) : undefined;
}

export function ProvidersSection({
  cfg,
  onChange,
}: {
  cfg: ConfigView;
  onChange: (next: ConfigView) => void;
}) {
  const providers = cfg.llm.providers ?? [];
  const decProviders = cfg.decisions.providers ?? [];
  const decModels = cfg.decisions.models ?? [];

  const addPresetProvider = (presetBuild: (n: number) => ProviderConfig) => {
    const template = presetBuild(providers.length + 1);
    const id = uniqueId(
      template.id,
      providers.map((p) => p.id),
    );
    const prefix = uniqueId(
      template.prefix,
      providers.map((p) => p.prefix),
    );
    onChange({
      ...cfg,
      llm: { ...cfg.llm, providers: [...providers, { ...template, id, prefix }] },
    });
  };

  const updateProvider = (idx: number, patch: Partial<ProviderConfig>) => {
    const next = [...providers];
    next[idx] = { ...next[idx], ...patch };
    onChange({ ...cfg, llm: { ...cfg.llm, providers: next } });
  };

  const removeProvider = (idx: number) => {
    onChange({
      ...cfg,
      llm: { ...cfg.llm, providers: providers.filter((_, i) => i !== idx) },
    });
  };

  const addDecisionProvider = () => {
    const id = uniqueId(
      "decisions",
      decProviders.map((p) => p.id),
    );
    const next: DecisionProviderConfig = {
      id,
      label: "OpenAI Decisions",
      protocol: "openai",
      base_url: "https://api.openai.com/v1",
      api_key_env: "OPENAI_API_KEY",
    };
    onChange({
      ...cfg,
      decisions: { ...cfg.decisions, providers: [...decProviders, next] },
    });
  };

  const updateDecisionProvider = (idx: number, patch: Partial<DecisionProviderConfig>) => {
    const next = [...decProviders];
    next[idx] = { ...next[idx], ...patch };
    onChange({ ...cfg, decisions: { ...cfg.decisions, providers: next } });
  };

  const addDecisionModel = () => {
    const pid = decProviders[0]?.id ?? "";
    const id = uniqueId(
      "decision-default",
      decModels.map((m) => m.id),
    );
    const next: DecisionModelConfig = {
      id,
      label: id,
      provider_id: pid,
      model: "gpt-4.1-mini",
    };
    onChange({
      ...cfg,
      decisions: { ...cfg.decisions, models: [...decModels, next] },
    });
  };

  const updateDecisionModel = (idx: number, patch: Partial<DecisionModelConfig>) => {
    const next = [...decModels];
    next[idx] = { ...next[idx], ...patch };
    onChange({ ...cfg, decisions: { ...cfg.decisions, models: next } });
  };

  return (
    <div className="space-y-5">
      {/* LLM Providers */}
      <Card>
        <CardHeader>
          <CardTitle>LLM Providers ({providers.length})</CardTitle>
          <CardDescription>
            Configure upstream providers visually using environment variable names (<code>*_env</code>) so secrets never live in <code>config.json</code>. Save to reload models.
          </CardDescription>
          <div className="flex flex-wrap gap-1.5 pt-2">
            {LLM_PRESETS.map((preset) => (
              <Button
                key={preset.label}
                type="button"
                variant="outline"
                size="sm"
                onClick={() => addPresetProvider(preset.build)}
              >
                <Plus className="h-3.5 w-3.5" />
                {preset.label}
              </Button>
            ))}
          </div>
        </CardHeader>
        <CardContent className="space-y-3">
          {providers.length === 0 ? (
            <div className="rounded-md border border-dashed border-border p-4 text-xs text-muted-foreground">
              No LLM providers configured yet. Click any preset above to add one.
            </div>
          ) : (
            providers.map((p, idx) => (
              <div
                key={`${p.id}-${idx}`}
                className="rounded-lg border border-border p-4 space-y-3 bg-background/50"
              >
                <div className="flex items-center justify-between gap-2">
                  <div className="flex items-center gap-2">
                    <Badge variant="secondary">{p.type}</Badge>
                    <span className="text-sm font-medium">{p.id || `Provider ${idx + 1}`}</span>
                  </div>
                  <div className="flex items-center gap-3">
                    <div className="flex items-center gap-2">
                      <Switch
                        checked={p.enabled}
                        onCheckedChange={(checked) => updateProvider(idx, { enabled: checked })}
                      />
                      <span className="text-xs text-muted-foreground">Enabled</span>
                    </div>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      onClick={() => removeProvider(idx)}
                      title="Remove provider"
                    >
                      <Trash2 className="h-4 w-4 text-destructive" />
                    </Button>
                  </div>
                </div>

                <div className="grid gap-3 md:grid-cols-4">
                  <div className="space-y-1">
                    <Label className="text-xs">ID</Label>
                    <Input
                      value={p.id}
                      onChange={(e) => updateProvider(idx, { id: e.target.value })}
                    />
                  </div>
                  <div className="space-y-1">
                    <Label className="text-xs">Type</Label>
                    <select
                      className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
                      value={p.type}
                      onChange={(e) => updateProvider(idx, { type: e.target.value })}
                    >
                      <option value="openai-compatible">openai-compatible</option>
                      <option value="openrouter">openrouter</option>
                      <option value="anthropic">anthropic</option>
                      <option value="grok">grok</option>
                      <option value="codex">codex</option>
                      <option value="chatgpt">chatgpt</option>
                    </select>
                  </div>
                  <div className="space-y-1">
                    <Label className="text-xs">Model Prefix</Label>
                    <Input
                      value={p.prefix}
                      onChange={(e) => updateProvider(idx, { prefix: e.target.value })}
                      placeholder="openai"
                    />
                  </div>
                  {p.type === "openai-compatible" && (
                    <div className="space-y-1">
                      <Label className="text-xs">Dialect Kind</Label>
                      <select
                        className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
                        value={p.kind ?? ""}
                        onChange={(e) =>
                          updateProvider(idx, { kind: e.target.value || undefined })
                        }
                      >
                        <option value="">auto-detect</option>
                        <option value="openai">openai</option>
                        <option value="generic">generic (no prompt_cache_key)</option>
                        <option value="openrouter">openrouter</option>
                        <option value="grok">grok</option>
                      </select>
                    </div>
                  )}
                </div>

                {p.type === "codex" ? (
                  <div className="grid gap-3 md:grid-cols-3">
                    <div className="space-y-1">
                      <Label className="text-xs">Codex Command</Label>
                      <Input
                        value={p.codex?.command ?? "codex"}
                        onChange={(e) =>
                          updateProvider(idx, {
                            codex: { ...p.codex, command: e.target.value || undefined },
                          })
                        }
                      />
                    </div>
                    <div className="space-y-1">
                      <Label className="text-xs">Working Directory</Label>
                      <Input
                        value={p.codex?.working_directory ?? ""}
                        placeholder="Optional workspace dir"
                        onChange={(e) =>
                          updateProvider(idx, {
                            codex: {
                              ...p.codex,
                              working_directory: e.target.value || undefined,
                            },
                          })
                        }
                      />
                    </div>
                    <div className="space-y-1">
                      <Label className="text-xs">Fallback Model</Label>
                      <Input
                        value={p.codex?.model ?? ""}
                        placeholder="Optional model"
                        onChange={(e) =>
                          updateProvider(idx, {
                            codex: { ...p.codex, model: e.target.value || undefined },
                          })
                        }
                      />
                    </div>
                  </div>
                ) : p.type === "chatgpt" ? (
                  <div className="grid gap-3 md:grid-cols-3">
                    <div className="space-y-1">
                      <Label className="text-xs">App ID</Label>
                      <Input
                        value={p.chatgpt?.app_id ?? ""}
                        onChange={(e) =>
                          updateProvider(idx, {
                            chatgpt: { ...p.chatgpt, app_id: e.target.value || undefined },
                          })
                        }
                      />
                    </div>
                    <div className="space-y-1">
                      <Label className="text-xs">App Name</Label>
                      <Input
                        value={p.chatgpt?.app_name ?? ""}
                        onChange={(e) =>
                          updateProvider(idx, {
                            chatgpt: { ...p.chatgpt, app_name: e.target.value || undefined },
                          })
                        }
                      />
                    </div>
                    <div className="space-y-1">
                      <Label className="text-xs">Auth State Directory</Label>
                      <Input
                        value={p.chatgpt?.directory ?? ""}
                        onChange={(e) =>
                          updateProvider(idx, {
                            chatgpt: { ...p.chatgpt, directory: e.target.value || undefined },
                          })
                        }
                      />
                    </div>
                  </div>
                ) : (
                  <div className="grid gap-3 md:grid-cols-3">
                    <div className="space-y-1">
                      <Label className="text-xs">Base URL</Label>
                      <Input
                        value={p.base_url ?? ""}
                        onChange={(e) =>
                          updateProvider(idx, { base_url: e.target.value || undefined })
                        }
                        placeholder="https://api.openai.com/v1"
                      />
                    </div>
                    <div className="space-y-1">
                      <Label className="text-xs">API Key Env Var</Label>
                      <Input
                        value={p.api_key_env ?? ""}
                        onChange={(e) =>
                          updateProvider(idx, { api_key_env: e.target.value || undefined })
                        }
                        placeholder="OPENAI_API_KEY"
                      />
                    </div>
                    <div className="space-y-1">
                      <Label className="text-xs">Explicit Models (comma-separated)</Label>
                      <Input
                        value={(p.models ?? []).join(", ")}
                        onChange={(e) => {
                          const list = e.target.value
                            .split(",")
                            .map((s) => s.trim())
                            .filter(Boolean);
                          updateProvider(idx, { models: list.length > 0 ? list : undefined });
                        }}
                        placeholder="Optional static model IDs"
                      />
                    </div>
                  </div>
                )}
              </div>
            ))
          )}
        </CardContent>
      </Card>

      {/* Decisions Providers & Models */}
      <Card>
        <CardHeader>
          <CardTitle>Decisions Providers &amp; Models</CardTitle>
          <CardDescription>
            Discrete judgment endpoints used for participation, memory gating, and tool approval <code>auto-detection</code>.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-5">
          <div className="space-y-2.5">
            <div className="flex items-center justify-between">
              <span className="text-xs font-semibold text-muted-foreground">
                Decisions Providers ({decProviders.length})
              </span>
              <Button type="button" variant="outline" size="sm" onClick={addDecisionProvider}>
                <Plus className="h-3.5 w-3.5" />
                Add Decisions Provider
              </Button>
            </div>
            {decProviders.map((dp, idx) => (
              <div
                key={`${dp.id}-${idx}`}
                className="rounded-md border border-border p-3 space-y-2.5 bg-background/50"
              >
                <div className="grid gap-2.5 md:grid-cols-5 items-end">
                  <div className="space-y-1">
                    <Label className="text-xs">ID</Label>
                    <Input
                      value={dp.id}
                      onChange={(e) => updateDecisionProvider(idx, { id: e.target.value })}
                    />
                  </div>
                  <div className="space-y-1">
                    <Label className="text-xs">Protocol</Label>
                    <select
                      className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
                      value={dp.protocol}
                      onChange={(e) =>
                        updateDecisionProvider(idx, {
                          protocol: e.target.value as "openai" | "jev" | "gemini",
                        })
                      }
                    >
                      <option value="openai">openai</option>
                      <option value="jev">jev</option>
                      <option value="gemini">gemini</option>
                    </select>
                  </div>
                  <div className="space-y-1 md:col-span-2">
                    <Label className="text-xs">Base URL</Label>
                    <Input
                      value={dp.base_url}
                      onChange={(e) => updateDecisionProvider(idx, { base_url: e.target.value })}
                    />
                  </div>
                  <div className="flex justify-end">
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      onClick={() =>
                        onChange({
                          ...cfg,
                          decisions: {
                            ...cfg.decisions,
                            providers: decProviders.filter((_, i) => i !== idx),
                          },
                        })
                      }
                    >
                      <Trash2 className="h-4 w-4 text-destructive" />
                    </Button>
                  </div>
                </div>
                <div className="grid gap-2.5 md:grid-cols-3">
                  <div className="space-y-1">
                    <Label className="text-xs">Label</Label>
                    <Input
                      value={dp.label ?? ""}
                      onChange={(e) =>
                        updateDecisionProvider(idx, { label: e.target.value || undefined })
                      }
                    />
                  </div>
                  <div className="space-y-1">
                    <Label className="text-xs">API Key Env Var</Label>
                    <Input
                      value={dp.api_key_env ?? ""}
                      onChange={(e) =>
                        updateDecisionProvider(idx, { api_key_env: e.target.value || undefined })
                      }
                    />
                  </div>
                  <div className="space-y-1">
                    <Label className="text-xs">Timeout (ms)</Label>
                    <Input
                      type="number"
                      min={0}
                      value={dp.timeout_ms ?? ""}
                      onChange={(e) =>
                        updateDecisionProvider(idx, { timeout_ms: parseIntOpt(e.target.value) })
                      }
                    />
                  </div>
                </div>
              </div>
            ))}
          </div>

          <div className="space-y-2.5 pt-2 border-t border-border">
            <div className="flex items-center justify-between">
              <span className="text-xs font-semibold text-muted-foreground">
                Decisions Models ({decModels.length})
              </span>
              <Button type="button" variant="outline" size="sm" onClick={addDecisionModel}>
                <Plus className="h-3.5 w-3.5" />
                Add Decisions Model
              </Button>
            </div>
            {decModels.map((dm, idx) => (
              <div
                key={`${dm.id}-${idx}`}
                className="grid gap-2.5 md:grid-cols-5 items-end rounded-md border border-border p-3 bg-background/50"
              >
                <div className="space-y-1">
                  <Label className="text-xs">Model Entry ID</Label>
                  <Input
                    value={dm.id}
                    onChange={(e) => updateDecisionModel(idx, { id: e.target.value })}
                  />
                </div>
                <div className="space-y-1">
                  <Label className="text-xs">Provider</Label>
                  <select
                    className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
                    value={dm.provider_id}
                    onChange={(e) => updateDecisionModel(idx, { provider_id: e.target.value })}
                  >
                    <option value="">Select provider…</option>
                    {decProviders.map((dp) => (
                      <option key={dp.id} value={dp.id}>
                        {dp.id} ({dp.protocol})
                      </option>
                    ))}
                  </select>
                </div>
                <div className="space-y-1">
                  <Label className="text-xs">Upstream Model Name</Label>
                  <Input
                    value={dm.model}
                    onChange={(e) => updateDecisionModel(idx, { model: e.target.value })}
                  />
                </div>
                <div className="space-y-1">
                  <Label className="text-xs">Context Window</Label>
                  <Input
                    type="number"
                    min={0}
                    value={dm.context_window ?? ""}
                    onChange={(e) =>
                      updateDecisionModel(idx, { context_window: parseIntOpt(e.target.value) })
                    }
                  />
                </div>
                <div className="flex justify-end">
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    onClick={() =>
                      onChange({
                        ...cfg,
                        decisions: {
                          ...cfg.decisions,
                          models: decModels.filter((_, i) => i !== idx),
                        },
                      })
                    }
                  >
                    <Trash2 className="h-4 w-4 text-destructive" />
                  </Button>
                </div>
              </div>
            ))}
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
