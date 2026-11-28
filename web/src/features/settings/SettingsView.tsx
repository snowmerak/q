import { useCallback, useEffect, useState } from "react";
import { Database, RefreshCw, Save } from "lucide-react";
import type { QClient } from "@/client/api";
import { QAPIError } from "@/client/api";
import type {
  ConfigView,
  DecisionModelInfo,
  HealthStatus,
  ModelInfo,
} from "@/client/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ModelSelect } from "@/components/ui/model-select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { ProvidersSection } from "@/features/settings/ProvidersSection";
import {
  EmbeddingsSection,
  StorageAndServerSection,
  ToolsAndMCPSection,
} from "@/features/settings/StorageAndToolsSection";

export function SettingsView({ client }: { client: QClient }) {
  const [health, setHealth] = useState<HealthStatus | null>(null);
  const [cfg, setCfg] = useState<ConfigView | null>(null);
  const [rawJson, setRawJson] = useState<string>("");
  const [models, setModels] = useState<ModelInfo[]>([]);
  const [decisionModels, setDecisionModels] = useState<DecisionModelInfo[]>([]);
  const [modelFilter, setModelFilter] = useState<string>("");
  const [statusMsg, setStatusMsg] = useState<string>("");
  const [restartFields, setRestartFields] = useState<string[]>([]);
  const [dirty, setDirty] = useState<boolean>(false);

  const loadAll = useCallback(async () => {
    try {
      const [h, c, m, dm] = await Promise.all([
        client.health(),
        client.getConfig(),
        client.listModels(),
        client.listDecisionModels(),
      ]);
      setHealth(h);
      setCfg(c);
      setRawJson(JSON.stringify(c, null, 2));
      setModels(m.models);
      setDecisionModels(dm.models);
      setDirty(false);
    } catch (err) {
      setStatusMsg(err instanceof Error ? err.message : "Failed to load settings");
    }
  }, [client]);

  useEffect(() => {
    void loadAll();
  }, [loadAll]);

  const updateCfg = (next: ConfigView) => {
    setCfg(next);
    setRawJson(JSON.stringify(next, null, 2));
    setDirty(true);
  };

  const handleSaveStructured = async () => {
    if (!cfg) return;
    setStatusMsg("");
    try {
      const res = await client.putConfig(cfg);
      setRestartFields(res.restart_required_fields);
      setStatusMsg("Configuration saved & applied.");
      await loadAll();
    } catch (err) {
      setStatusMsg(err instanceof QAPIError ? `${err.code}: ${err.message}` : "Save failed");
    }
  };

  const handleSaveRawJson = async () => {
    setStatusMsg("");
    try {
      const parsed = JSON.parse(rawJson) as ConfigView;
      const res = await client.putConfig(parsed);
      setRestartFields(res.restart_required_fields);
      setStatusMsg("Raw JSON configuration saved.");
      await loadAll();
    } catch (err) {
      setStatusMsg(err instanceof Error ? err.message : "Invalid JSON");
    }
  };

  const handleRefreshModels = async () => {
    setStatusMsg("");
    try {
      const res = await client.refreshModels();
      setStatusMsg(`Refreshed ${res.count} models.`);
      await loadAll();
    } catch (err) {
      setStatusMsg(err instanceof Error ? err.message : "Refresh failed");
    }
  };

  const handleReindex = async () => {
    setStatusMsg("");
    try {
      const res = await client.reindexStorage();
      setStatusMsg(`Reindex started (job ${res.job_id.slice(0, 8)}).`);
    } catch (err) {
      setStatusMsg(err instanceof Error ? err.message : "Reindex failed");
    }
  };

  const llmModelOptions = models.map((m) => ({
    id: m.id,
    label: m.context_length ? `${m.id} · ctx ${m.context_length}` : m.id,
  }));

  const decisionOptions = [
    ...decisionModels.map((dm) => ({
      id: dm.id,
      label: `${dm.id} (${dm.protocol})`,
    })),
    ...(cfg?.decisions.models ?? []).map((dm) => ({
      id: dm.id,
      label: dm.label ? `${dm.id} — ${dm.label}` : dm.id,
    })),
  ];

  const filteredModels = models.filter((m) =>
    modelFilter.trim() === ""
      ? true
      : m.id.toLowerCase().includes(modelFilter.trim().toLowerCase()),
  );

  return (
    <div className="flex-1 overflow-auto p-6">
      <div className="mx-auto max-w-5xl space-y-5">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h1 className="text-xl font-semibold tracking-tight">Settings &amp; Configuration</h1>
            <p className="text-sm text-muted-foreground">
              Manage LLM providers, model roles, embeddings, Decisions, tools/MCP, and storage backends.
            </p>
          </div>
          <div className="flex items-center gap-2">
            {statusMsg && <Badge variant="outline">{statusMsg}</Badge>}
            {cfg && (
              <Button onClick={() => void handleSaveStructured()}>
                <Save className="h-4 w-4" />
                {dirty ? "Save Changes *" : "Save Changes"}
              </Button>
            )}
          </div>
        </div>

        {restartFields.length > 0 && (
          <div className="rounded-lg border border-amber-500/40 bg-amber-500/10 px-4 py-2.5 text-xs">
            Restart required for changed fields: <code>{restartFields.join(", ")}</code>
          </div>
        )}

        {/* Health & Maintenance */}
        <Card>
          <CardHeader className="flex flex-row items-center justify-between gap-4">
            <div>
              <CardTitle>System &amp; Storage Status</CardTitle>
              <CardDescription>
                {health?.reason || "All configured core services and storage backends"}
              </CardDescription>
            </div>
            <div className="flex items-center gap-2">
              <Button variant="outline" size="sm" onClick={() => void handleRefreshModels()}>
                <RefreshCw className="h-4 w-4" />
                Refresh Models
              </Button>
              <Button variant="outline" size="sm" onClick={() => void handleReindex()}>
                <Database className="h-4 w-4" />
                Reindex Search
              </Button>
            </div>
          </CardHeader>
          <CardContent className="flex flex-wrap gap-2">
            <Badge variant={health?.status === "ready" ? "default" : "destructive"}>
              Overall: {health?.status ?? "unknown"}
            </Badge>
            <Badge variant="secondary">RDB: {health?.rdb ?? "—"}</Badge>
            <Badge variant="secondary">Search: {health?.search ?? "—"}</Badge>
            <Badge variant="secondary">Blob: {health?.blob ?? "—"}</Badge>
          </CardContent>
        </Card>

        {cfg && (
          <Tabs defaultValue="roles">
            <TabsList className="flex flex-wrap h-auto">
              <TabsTrigger value="roles">Models &amp; Roles ({models.length})</TabsTrigger>
              <TabsTrigger value="providers">
                Providers ({cfg.llm.providers.length})
              </TabsTrigger>
              <TabsTrigger value="embeddings">
                Embeddings {cfg.embeddings.enabled ? "(On)" : "(Off)"}
              </TabsTrigger>
              <TabsTrigger value="tools">Tools &amp; MCP</TabsTrigger>
              <TabsTrigger value="storage">Storage &amp; Server</TabsTrigger>
              <TabsTrigger value="json">Raw config.json</TabsTrigger>
            </TabsList>

            <TabsContent value="roles" className="space-y-5">
              <Card>
                <CardHeader>
                  <CardTitle>Default Role Models</CardTitle>
                  <CardDescription>
                    Select default models discovered from enabled providers. Configure providers in the Providers tab first if this list is empty.
                  </CardDescription>
                </CardHeader>
                <CardContent className="space-y-4">
                  <div className="grid gap-4 md:grid-cols-2">
                    <div className="space-y-1.5">
                      <Label htmlFor="role-chat">Default Chat Model</Label>
                      <ModelSelect
                        id="role-chat"
                        value={cfg.llm.roles.chat_model ?? ""}
                        options={llmModelOptions}
                        emptyLabel="— Select chat model —"
                        onChange={(v) =>
                          updateCfg({
                            ...cfg,
                            llm: {
                              ...cfg.llm,
                              roles: { ...cfg.llm.roles, chat_model: v || undefined },
                            },
                          })
                        }
                      />
                    </div>
                    <div className="space-y-1.5">
                      <Label htmlFor="role-mem">Default Memory Model</Label>
                      <ModelSelect
                        id="role-mem"
                        value={cfg.llm.roles.memory_model ?? ""}
                        options={llmModelOptions}
                        emptyLabel="— Same as Chat Model —"
                        onChange={(v) =>
                          updateCfg({
                            ...cfg,
                            llm: {
                              ...cfg.llm,
                              roles: { ...cfg.llm.roles, memory_model: v || undefined },
                            },
                          })
                        }
                      />
                    </div>
                    <div className="space-y-1.5">
                      <Label htmlFor="role-recall">Recall Planner Model</Label>
                      <ModelSelect
                        id="role-recall"
                        value={cfg.llm.roles.recall_planner_model ?? ""}
                        options={llmModelOptions}
                        emptyLabel="— Same as Memory Model —"
                        onChange={(v) =>
                          updateCfg({
                            ...cfg,
                            llm: {
                              ...cfg.llm,
                              roles: { ...cfg.llm.roles, recall_planner_model: v || undefined },
                            },
                          })
                        }
                      />
                    </div>
                    <div className="space-y-1.5">
                      <Label htmlFor="role-sub">Subagent Model</Label>
                      <ModelSelect
                        id="role-sub"
                        value={cfg.llm.roles.subagent_model ?? ""}
                        options={llmModelOptions}
                        emptyLabel="— Same as Chat Model —"
                        onChange={(v) =>
                          updateCfg({
                            ...cfg,
                            llm: {
                              ...cfg.llm,
                              roles: { ...cfg.llm.roles, subagent_model: v || undefined },
                            },
                          })
                        }
                      />
                    </div>
                    <div className="space-y-1.5">
                      <Label htmlFor="decisions-default-model">Decisions Default Model</Label>
                      <ModelSelect
                        id="decisions-default-model"
                        value={cfg.decisions.default_model_id ?? ""}
                        options={decisionOptions}
                        emptyLabel="— No Decisions default —"
                        customPlaceholder="decisions-model-id"
                        onChange={(v) =>
                          updateCfg({
                            ...cfg,
                            decisions: { ...cfg.decisions, default_model_id: v || undefined },
                          })
                        }
                      />
                    </div>
                  </div>

                  <div className="grid gap-4 md:grid-cols-2 pt-2 border-t border-border">
                    <div className="space-y-1.5">
                      <Label htmlFor="llm-refresh-interval">Model Cache Refresh Interval</Label>
                      <Input
                        id="llm-refresh-interval"
                        value={cfg.llm.model_cache_refresh_interval ?? ""}
                        onChange={(e) =>
                          updateCfg({
                            ...cfg,
                            llm: {
                              ...cfg.llm,
                              model_cache_refresh_interval: e.target.value || undefined,
                            },
                          })
                        }
                        placeholder="5m"
                      />
                    </div>
                    <div className="space-y-1.5">
                      <Label htmlFor="llm-refresh-timeout">Model Cache Refresh Timeout</Label>
                      <Input
                        id="llm-refresh-timeout"
                        value={cfg.llm.model_cache_refresh_timeout ?? ""}
                        onChange={(e) =>
                          updateCfg({
                            ...cfg,
                            llm: {
                              ...cfg.llm,
                              model_cache_refresh_timeout: e.target.value || undefined,
                            },
                          })
                        }
                        placeholder="15s"
                      />
                    </div>
                  </div>
                </CardContent>
              </Card>

              <Card>
                <CardHeader className="flex flex-row items-center justify-between gap-4">
                  <div>
                    <CardTitle>Discovered Models ({filteredModels.length})</CardTitle>
                    <CardDescription>
                      Live model capabilities cached from <code>pkg/llm/gateway</code>.
                    </CardDescription>
                  </div>
                  <Input
                    className="w-60"
                    placeholder="Filter models…"
                    value={modelFilter}
                    onChange={(e) => setModelFilter(e.target.value)}
                  />
                </CardHeader>
                <CardContent>
                  <div className="max-h-72 overflow-auto rounded-md border border-border divide-y divide-border text-xs">
                    {filteredModels.length === 0 ? (
                      <div className="p-4 text-muted-foreground">
                        {models.length === 0
                          ? "No enabled LLM providers or models discovered yet. Add providers in the Providers tab and save."
                          : "No models match your filter."}
                      </div>
                    ) : (
                      filteredModels.map((m) => (
                        <div key={m.id} className="flex items-center justify-between px-3 py-2">
                          <span className="font-mono">{m.id}</span>
                          <div className="flex items-center gap-2 text-muted-foreground">
                            {m.context_length ? <span>ctx {m.context_length}</span> : null}
                            {m.capabilities?.tool_calling && (
                              <Badge variant="outline">tools</Badge>
                            )}
                            {m.capabilities?.reasoning?.supported && (
                              <Badge variant="outline">reasoning</Badge>
                            )}
                          </div>
                        </div>
                      ))
                    )}
                  </div>
                </CardContent>
              </Card>
            </TabsContent>

            <TabsContent value="providers">
              <ProvidersSection cfg={cfg} onChange={updateCfg} />
            </TabsContent>

            <TabsContent value="embeddings">
              <EmbeddingsSection
                cfg={cfg}
                onChange={updateCfg}
                onReindex={() => void handleReindex()}
              />
            </TabsContent>

            <TabsContent value="tools">
              <ToolsAndMCPSection cfg={cfg} onChange={updateCfg} />
            </TabsContent>

            <TabsContent value="storage">
              <StorageAndServerSection cfg={cfg} onChange={updateCfg} />
            </TabsContent>

            <TabsContent value="json">
              <Card>
                <CardHeader>
                  <CardTitle>Raw config.json Editor</CardTitle>
                  <CardDescription>
                    Inspect or edit the full JSON document directly. Secret values remain in environment variables (<code>*_env</code>).
                  </CardDescription>
                </CardHeader>
                <CardContent className="space-y-3">
                  <Textarea
                    className="min-h-[420px] font-mono text-xs"
                    value={rawJson}
                    onChange={(e) => setRawJson(e.target.value)}
                  />
                  <div className="flex justify-end">
                    <Button onClick={() => void handleSaveRawJson()}>
                      <Save className="h-4 w-4" />
                      Apply Raw JSON
                    </Button>
                  </div>
                </CardContent>
              </Card>
            </TabsContent>
          </Tabs>
        )}
      </div>
    </div>
  );
}
