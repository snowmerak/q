import { useState } from "react";
import type {
  ConfigView,
  ModelBehaviorConfig,
  ModelCapabilities,
  ModelInfo,
  ModelMetadataConfig,
  ModelSamplingConfig,
  ProviderConfig,
} from "@/client/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";

const SAMPLING_FIELDS: Array<{
  key: keyof ModelSamplingConfig;
  min: number;
  max?: number;
  step: string;
  hint: string;
}> = [
  { key: "temperature", min: 0, step: "any", hint: "0 selects the most likely token" },
  { key: "top_p", min: 0, max: 1, step: "any", hint: "1 disables cumulative probability filtering" },
  { key: "top_k", min: 0, step: "1", hint: "0 disables top-K limit" },
  { key: "min_p", min: 0, max: 1, step: "any", hint: "0 disables minimum probability filtering" },
  { key: "repetition_penalty", min: 0.01, step: "any", hint: "1 disables repetition suppression (> 0)" },
  { key: "presence_penalty", min: -2, max: 2, step: "any", hint: "0 disables presence penalty (-2..2)" },
  { key: "frequency_penalty", min: -2, max: 2, step: "any", hint: "0 disables frequency penalty (-2..2)" },
];

function parseIntOpt(raw: string): number | undefined {
  const trimmed = raw.trim();
  if (!trimmed) return undefined;
  const parsed = Number(trimmed);
  return Number.isFinite(parsed) ? Math.trunc(parsed) : undefined;
}

function parseFloatOpt(raw: string): number | undefined {
  const trimmed = raw.trim();
  if (!trimmed) return undefined;
  const parsed = Number(trimmed);
  return Number.isFinite(parsed) ? parsed : undefined;
}

function findOwningProvider(
  prefixedId: string,
  providers: ProviderConfig[],
): { providerIndex: number; upstreamId: string } | null {
  const slash = prefixedId.indexOf("/");
  if (slash <= 0) return null;
  const prefix = prefixedId.slice(0, slash);
  const upstreamId = prefixedId.slice(slash + 1);
  if (!upstreamId) return null;
  const providerIndex = providers.findIndex((p) => (p.prefix || p.id) === prefix);
  if (providerIndex < 0) return null;
  return { providerIndex, upstreamId };
}

function cleanMetadata(meta: ModelMetadataConfig): ModelMetadataConfig | undefined {
  const caps = meta.capabilities ? { ...meta.capabilities } : undefined;
  if (caps) {
    if (caps.tool_calling === undefined) delete caps.tool_calling;
    if (caps.reasoning === undefined) delete caps.reasoning;
  }
  const hasCaps = Boolean(caps && Object.keys(caps).length > 0);
  const next: ModelMetadataConfig = {
    ...(meta.context_length && meta.context_length > 0 ? { context_length: meta.context_length } : {}),
    ...(meta.max_output_tokens && meta.max_output_tokens > 0 ? { max_output_tokens: meta.max_output_tokens } : {}),
    ...(hasCaps ? { capabilities: caps } : {}),
  };
  return Object.keys(next).length > 0 ? next : undefined;
}

function cleanSampling(sampling: ModelSamplingConfig | undefined): ModelSamplingConfig | undefined {
  if (!sampling) return undefined;
  const next: ModelSamplingConfig = {};
  for (const field of SAMPLING_FIELDS) {
    const val = sampling[field.key];
    if (typeof val === "number" && Number.isFinite(val)) {
      next[field.key] = val;
    }
  }
  return Object.keys(next).length > 0 ? next : undefined;
}

function cleanBehavior(b: ModelBehaviorConfig): ModelBehaviorConfig | undefined {
  const sampling = cleanSampling(b.sampling);
  const next: ModelBehaviorConfig = {
    ...(b.output_limit && b.output_limit > 0 ? { output_limit: b.output_limit } : {}),
    ...(b.output_token_field === "max_tokens" ? { output_token_field: "max_tokens" } : {}),
    ...(b.allow_missing_finish_reason ? { allow_missing_finish_reason: true } : {}),
    ...(sampling ? { sampling } : {}),
  };
  return Object.keys(next).length > 0 ? next : undefined;
}

export function ModelBehaviorSection({
  cfg,
  models,
  onChange,
}: {
  cfg: ConfigView;
  models: ModelInfo[];
  onChange: (next: ConfigView) => void;
}) {
  const [filter, setFilter] = useState("");
  const providers = cfg.llm.providers ?? [];
  const behaviors = cfg.llm.model_behaviors ?? {};

  // Build deduplicated list of prefixed model IDs from live discovery,
  // provider metadata overrides, and model behavior entries.
  const discoveredMap = new Map<string, ModelInfo>();
  for (const m of models) {
    discoveredMap.set(m.id, m);
  }
  const allIds = new Set<string>(models.map((m) => m.id));
  for (const p of providers) {
    const prefix = p.prefix || p.id;
    if (!prefix) continue;
    for (const upstream of Object.keys(p.model_metadata ?? {})) {
      allIds.add(`${prefix}/${upstream}`);
    }
  }
  for (const id of Object.keys(behaviors)) {
    allIds.add(id);
  }

  const normalizedFilter = filter.trim().toLowerCase();
  const visibleIds = Array.from(allIds).filter((id) =>
    normalizedFilter === "" ? true : id.toLowerCase().includes(normalizedFilter),
  );

  const updateMetadata = (prefixedId: string, updater: (cur: ModelMetadataConfig) => ModelMetadataConfig) => {
    const owner = findOwningProvider(prefixedId, providers);
    if (!owner) return;
    const provider = providers[owner.providerIndex];
    const currentMap = provider.model_metadata ?? {};
    const cur = currentMap[owner.upstreamId] ?? {};
    const updated = cleanMetadata(updater(cur));
    const nextMap = { ...currentMap };
    if (updated) {
      nextMap[owner.upstreamId] = updated;
    } else {
      delete nextMap[owner.upstreamId];
    }
    const nextProviders = [...providers];
    nextProviders[owner.providerIndex] = {
      ...provider,
      model_metadata: Object.keys(nextMap).length > 0 ? nextMap : undefined,
    };
    onChange({
      ...cfg,
      llm: { ...cfg.llm, providers: nextProviders },
    });
  };

  const updateBehavior = (prefixedId: string, updater: (cur: ModelBehaviorConfig) => ModelBehaviorConfig) => {
    const cur = behaviors[prefixedId] ?? {};
    const updated = cleanBehavior(updater(cur));
    const nextBehaviors = { ...behaviors };
    if (updated) {
      nextBehaviors[prefixedId] = updated;
    } else {
      delete nextBehaviors[prefixedId];
    }
    onChange({
      ...cfg,
      llm: {
        ...cfg.llm,
        model_behaviors: Object.keys(nextBehaviors).length > 0 ? nextBehaviors : undefined,
      },
    });
  };

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between gap-4">
        <div>
          <CardTitle>Model Behavior &amp; Metadata Overrides ({visibleIds.length})</CardTitle>
          <CardDescription>
            Override missing or inaccurate upstream <code>context_length</code>, <code>max_output_tokens</code>, reasoning capabilities, output limits, legacy <code>max_tokens</code> field, finish-reason handling, and sampling defaults per model.
          </CardDescription>
        </div>
        <Input
          className="w-60"
          placeholder="Filter models…"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
        />
      </CardHeader>
      <CardContent className="space-y-4">
        {visibleIds.length === 0 ? (
          <div className="rounded-md border border-dashed border-border p-4 text-xs text-muted-foreground">
            {allIds.size === 0
              ? "No models discovered yet. Enable an LLM provider in the Providers tab and save/refresh models."
              : "No models match your filter."}
          </div>
        ) : (
          visibleIds.map((prefixedId) => {
            const owner = findOwningProvider(prefixedId, providers);
            const meta: ModelMetadataConfig = owner
              ? providers[owner.providerIndex].model_metadata?.[owner.upstreamId] ?? {}
              : {};
            const live = discoveredMap.get(prefixedId);
            const beh = behaviors[prefixedId] ?? {};
            const rc = meta.capabilities?.reasoning;
            const control = rc?.control ?? "";

            const setReasoningControl = (nextControl: string) => {
              updateMetadata(prefixedId, (cur) => {
                const caps: ModelCapabilities = { ...(cur.capabilities ?? {}) };
                if (!nextControl) {
                  delete caps.reasoning;
                  return { ...cur, capabilities: caps };
                }
                if (nextControl === "effort") {
                  const prev = caps.reasoning;
                  const efforts = prev?.supported_efforts?.length ? prev.supported_efforts : undefined;
                  const def =
                    prev?.default_effort && (!efforts || efforts.includes(prev.default_effort))
                      ? prev.default_effort
                      : undefined;
                  caps.reasoning = {
                    supported: true,
                    control: "effort",
                    ...(efforts ? { supported_efforts: efforts } : {}),
                    ...(def ? { default_effort: def } : {}),
                    ...(prev?.mandatory ? { mandatory: true } : {}),
                  };
                } else if (nextControl === "token_budget") {
                  caps.reasoning = {
                    supported: true,
                    control: "token_budget",
                    supports_max_tokens: true,
                  };
                } else {
                  caps.reasoning = {
                    supported: true,
                    control: nextControl,
                  };
                }
                return { ...cur, capabilities: caps };
              });
            };

            return (
              <div
                key={prefixedId}
                className="rounded-lg border border-border p-4 space-y-4 bg-background/50"
              >
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div>
                    <div className="font-mono text-sm font-semibold">{prefixedId}</div>
                    <div className="text-xs text-muted-foreground">
                      {live?.context_length
                        ? `Effective context: ${live.context_length.toLocaleString()}`
                        : "Effective context: 32,768 (fallback — no upstream context_length advertised)"}
                      {live?.max_output_tokens
                        ? ` · Max output: ${live.max_output_tokens.toLocaleString()}`
                        : ""}
                    </div>
                  </div>
                  <div className="flex flex-wrap gap-1.5">
                    {Object.keys(meta).length > 0 && <Badge variant="secondary">metadata override</Badge>}
                    {Object.keys(beh).length > 0 && <Badge variant="secondary">behavior override</Badge>}
                  </div>
                </div>

                {/* Provider ModelMetadata overrides */}
                <div className="grid gap-3 md:grid-cols-4">
                  <div className="space-y-1">
                    <Label className="text-xs">Context Length Override</Label>
                    <Input
                      type="number"
                      min={0}
                      disabled={!owner}
                      value={meta.context_length ?? ""}
                      placeholder={
                        live?.context_length ? String(live.context_length) : "e.g. 131072"
                      }
                      onChange={(e) =>
                        updateMetadata(prefixedId, (cur) => ({
                          ...cur,
                          context_length: parseIntOpt(e.target.value),
                        }))
                      }
                    />
                  </div>
                  <div className="space-y-1">
                    <Label className="text-xs">Max Output Metadata</Label>
                    <Input
                      type="number"
                      min={0}
                      disabled={!owner}
                      value={meta.max_output_tokens ?? ""}
                      placeholder={
                        live?.max_output_tokens ? String(live.max_output_tokens) : "e.g. 16384"
                      }
                      onChange={(e) =>
                        updateMetadata(prefixedId, (cur) => ({
                          ...cur,
                          max_output_tokens: parseIntOpt(e.target.value),
                        }))
                      }
                    />
                  </div>
                  <div className="space-y-1">
                    <Label className="text-xs">Tool Calling Support</Label>
                    <select
                      disabled={!owner}
                      className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
                      value={
                        meta.capabilities?.tool_calling === undefined
                          ? ""
                          : meta.capabilities.tool_calling
                            ? "true"
                            : "false"
                      }
                      onChange={(e) => {
                        const val = e.target.value;
                        updateMetadata(prefixedId, (cur) => {
                          const caps: ModelCapabilities = { ...(cur.capabilities ?? {}) };
                          if (val === "") delete caps.tool_calling;
                          else caps.tool_calling = val === "true";
                          return { ...cur, capabilities: caps };
                        });
                      }}
                    >
                      <option value="">Inherit / Auto</option>
                      <option value="true">Supported</option>
                      <option value="false">Unsupported</option>
                    </select>
                  </div>
                  <div className="space-y-1">
                    <Label className="text-xs">Reasoning Control</Label>
                    <select
                      disabled={!owner}
                      className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
                      value={control}
                      onChange={(e) => setReasoningControl(e.target.value)}
                    >
                      <option value="">Inherit / Auto</option>
                      <option value="effort">effort</option>
                      <option value="toggle">toggle</option>
                      <option value="token_budget">token_budget</option>
                      <option value="fixed">fixed</option>
                    </select>
                  </div>
                </div>

                {control === "effort" && (
                  <div className="grid gap-3 md:grid-cols-3 items-end rounded-md border border-border p-3 bg-muted/20">
                    <div className="space-y-1">
                      <Label className="text-xs">Supported Efforts (comma-separated)</Label>
                      <Input
                        value={(rc?.supported_efforts ?? []).join(", ")}
                        placeholder="minimal, low, medium, high"
                        onChange={(e) => {
                          const raw = e.target.value
                            .split(",")
                            .map((s) => s.trim())
                            .filter(Boolean);
                          const unique = Array.from(new Set(raw));
                          updateMetadata(prefixedId, (cur) => {
                            const prev = cur.capabilities?.reasoning;
                            const nextDefault =
                              prev?.default_effort &&
                              (unique.length === 0 || unique.includes(prev.default_effort))
                                ? prev.default_effort
                                : undefined;
                            return {
                              ...cur,
                              capabilities: {
                                ...(cur.capabilities ?? {}),
                                reasoning: {
                                  supported: true,
                                  control: "effort",
                                  ...(unique.length > 0 ? { supported_efforts: unique } : {}),
                                  ...(nextDefault ? { default_effort: nextDefault } : {}),
                                  ...(prev?.mandatory ? { mandatory: true } : {}),
                                },
                              },
                            };
                          });
                        }}
                      />
                    </div>
                    <div className="space-y-1">
                      <Label className="text-xs">Default Effort (optional)</Label>
                      <Input
                        value={rc?.default_effort ?? ""}
                        placeholder={
                          rc?.supported_efforts?.length
                            ? `One of: ${rc.supported_efforts.join(", ")}`
                            : "e.g. medium"
                        }
                        onChange={(e) => {
                          const candidate = e.target.value.trim();
                          updateMetadata(prefixedId, (cur) => {
                            const prev = cur.capabilities?.reasoning;
                            const efforts = prev?.supported_efforts;
                            const validDefault =
                              candidate && (!efforts?.length || efforts.includes(candidate))
                                ? candidate
                                : undefined;
                            return {
                              ...cur,
                              capabilities: {
                                ...(cur.capabilities ?? {}),
                                reasoning: {
                                  supported: true,
                                  control: "effort",
                                  ...(efforts?.length ? { supported_efforts: efforts } : {}),
                                  ...(validDefault ? { default_effort: validDefault } : {}),
                                  ...(prev?.mandatory ? { mandatory: true } : {}),
                                },
                              },
                            };
                          });
                        }}
                      />
                    </div>
                    <div className="flex items-center gap-2 pb-2">
                      <Switch
                        checked={Boolean(rc?.mandatory)}
                        onCheckedChange={(checked) =>
                          updateMetadata(prefixedId, (cur) => ({
                            ...cur,
                            capabilities: {
                              ...(cur.capabilities ?? {}),
                              reasoning: {
                                supported: true,
                                control: "effort",
                                ...(cur.capabilities?.reasoning?.supported_efforts?.length
                                  ? { supported_efforts: cur.capabilities.reasoning.supported_efforts }
                                  : {}),
                                ...(cur.capabilities?.reasoning?.default_effort
                                  ? { default_effort: cur.capabilities.reasoning.default_effort }
                                  : {}),
                                ...(checked ? { mandatory: true } : {}),
                              },
                            },
                          }))
                        }
                      />
                      <span className="text-xs">Mandatory reasoning</span>
                    </div>
                  </div>
                )}

                {/* Request behavior overrides */}
                <div className="grid gap-3 md:grid-cols-3 pt-2 border-t border-border items-end">
                  <div className="space-y-1">
                    <Label className="text-xs">Request Output Limit</Label>
                    <Input
                      type="number"
                      min={1}
                      value={beh.output_limit ?? ""}
                      placeholder="Request / provider default"
                      onChange={(e) =>
                        updateBehavior(prefixedId, (cur) => ({
                          ...cur,
                          output_limit: parseIntOpt(e.target.value),
                        }))
                      }
                    />
                  </div>
                  <div className="space-y-1">
                    <Label className="text-xs">Chat Token Parameter</Label>
                    <select
                      className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
                      value={beh.output_token_field ?? ""}
                      onChange={(e) =>
                        updateBehavior(prefixedId, (cur) => ({
                          ...cur,
                          output_token_field: e.target.value as "" | "max_tokens",
                        }))
                      }
                    >
                      <option value="">Standard (max_completion_tokens)</option>
                      <option value="max_tokens">Legacy (max_tokens)</option>
                    </select>
                  </div>
                  <label className="flex items-center gap-2 pb-2 text-xs cursor-pointer select-none">
                    <input
                      type="checkbox"
                      checked={Boolean(beh.allow_missing_finish_reason)}
                      onChange={(e) =>
                        updateBehavior(prefixedId, (cur) => ({
                          ...cur,
                          allow_missing_finish_reason: e.target.checked,
                        }))
                      }
                    />
                    <span>Allow missing finish reason</span>
                  </label>
                </div>

                {/* Sampling overrides */}
                <details className="rounded-md border border-border p-3 bg-muted/10">
                  <summary className="cursor-pointer text-xs font-medium text-muted-foreground">
                    Sampling overrides
                    {cleanSampling(beh.sampling) ? " (active)" : ""}
                  </summary>
                  <div className="mt-3 space-y-3">
                    <div className="flex flex-wrap gap-2">
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        onClick={() =>
                          updateBehavior(prefixedId, (cur) => ({
                            ...cur,
                            sampling: {
                              temperature: 0,
                              top_p: 1,
                              top_k: 0,
                              min_p: 0,
                              repetition_penalty: 1,
                              presence_penalty: 0,
                              frequency_penalty: 0,
                            },
                          }))
                        }
                      >
                        Greedy / penalties off
                      </Button>
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        onClick={() =>
                          updateBehavior(prefixedId, (cur) => ({
                            ...cur,
                            sampling: undefined,
                          }))
                        }
                      >
                        Clear sampling overrides
                      </Button>
                    </div>
                    <div className="grid gap-3 md:grid-cols-4">
                      {SAMPLING_FIELDS.map((field) => (
                        <div key={field.key} className="space-y-1">
                          <Label className="text-xs font-mono">{field.key}</Label>
                          <Input
                            type="number"
                            min={field.min}
                            max={field.max}
                            step={field.step}
                            placeholder="Default"
                            value={beh.sampling?.[field.key] ?? ""}
                            onChange={(e) => {
                              const num = parseFloatOpt(e.target.value);
                              updateBehavior(prefixedId, (cur) => ({
                                ...cur,
                                sampling: {
                                  ...(cur.sampling ?? {}),
                                  [field.key]: num,
                                },
                              }));
                            }}
                          />
                          <p className="text-[11px] text-muted-foreground">{field.hint}</p>
                        </div>
                      ))}
                    </div>
                  </div>
                </details>
              </div>
            );
          })
        )}
      </CardContent>
    </Card>
  );
}
