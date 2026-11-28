import { useCallback, useEffect, useState } from "react";
import { Database, RefreshCw, Save } from "lucide-react";
import type { QClient } from "@/client/api";
import { QAPIError } from "@/client/api";
import type {
  ApprovalMode,
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
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";

export function SettingsView({ client }: { client: QClient }) {
  const [health, setHealth] = useState<HealthStatus | null>(null);
  const [cfg, setCfg] = useState<ConfigView | null>(null);
  const [rawJson, setRawJson] = useState<string>("");
  const [models, setModels] = useState<ModelInfo[]>([]);
  const [decisionModels, setDecisionModels] = useState<DecisionModelInfo[]>([]);
  const [statusMsg, setStatusMsg] = useState<string>("");
  const [restartFields, setRestartFields] = useState<string[]>([]);

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
    } catch (err) {
      setStatusMsg(err instanceof Error ? err.message : "Failed to load settings");
    }
  }, [client]);

  useEffect(() => {
    void loadAll();
  }, [loadAll]);

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

  return (
    <div className="flex-1 overflow-auto p-6">
      <div className="mx-auto max-w-5xl space-y-5">
        <div className="flex items-center justify-between">
          <div>
            <h1 className="text-xl font-semibold tracking-tight">Settings &amp; Models</h1>
            <p className="text-sm text-muted-foreground">
              Manage default LLM roles, approval gate policy, browser driver preference, and storage health.
            </p>
          </div>
          {statusMsg && <Badge variant="outline">{statusMsg}</Badge>}
        </div>

        {restartFields.length > 0 && (
          <div className="rounded-lg border border-amber-500/40 bg-amber-500/10 px-4 py-2.5 text-xs">
            Restart required for changed fields: <code>{restartFields.join(", ")}</code>
          </div>
        )}

        {/* Health & Maintenance */}
        <Card>
          <CardHeader className="flex flex-row items-center justify-between">
            <div>
              <CardTitle>System &amp; Storage Status</CardTitle>
              <CardDescription>
                {health?.reason || "All configured core services and storage backends"}
              </CardDescription>
            </div>
            <Button variant="outline" size="sm" onClick={() => void handleReindex()}>
              <Database className="h-4 w-4" />
              Reindex Search
            </Button>
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
          <Tabs defaultValue="general">
            <TabsList>
              <TabsTrigger value="general">Roles &amp; Tools</TabsTrigger>
              <TabsTrigger value="models">Discovered Models ({models.length})</TabsTrigger>
              <TabsTrigger value="json">Raw config.json</TabsTrigger>
            </TabsList>

            <TabsContent value="general">
              <Card>
                <CardContent className="space-y-5 pt-5">
                  <div className="grid gap-4 md:grid-cols-2">
                    <div className="space-y-1.5">
                      <Label htmlFor="role-chat">Default Chat Model</Label>
                      <Input
                        id="role-chat"
                        list="settings-models"
                        value={cfg.llm.roles.chat_model ?? ""}
                        onChange={(e) =>
                          setCfg({
                            ...cfg,
                            llm: {
                              ...cfg.llm,
                              roles: { ...cfg.llm.roles, chat_model: e.target.value },
                            },
                          })
                        }
                      />
                    </div>
                    <div className="space-y-1.5">
                      <Label htmlFor="role-mem">Default Memory Model</Label>
                      <Input
                        id="role-mem"
                        list="settings-models"
                        value={cfg.llm.roles.memory_model ?? ""}
                        onChange={(e) =>
                          setCfg({
                            ...cfg,
                            llm: {
                              ...cfg.llm,
                              roles: { ...cfg.llm.roles, memory_model: e.target.value },
                            },
                          })
                        }
                      />
                    </div>
                    <div className="space-y-1.5">
                      <Label htmlFor="role-recall">Recall Planner Model</Label>
                      <Input
                        id="role-recall"
                        list="settings-models"
                        value={cfg.llm.roles.recall_planner_model ?? ""}
                        onChange={(e) =>
                          setCfg({
                            ...cfg,
                            llm: {
                              ...cfg.llm,
                              roles: { ...cfg.llm.roles, recall_planner_model: e.target.value },
                            },
                          })
                        }
                      />
                    </div>
                    <div className="space-y-1.5">
                      <Label htmlFor="role-sub">Subagent Model</Label>
                      <Input
                        id="role-sub"
                        list="settings-models"
                        value={cfg.llm.roles.subagent_model ?? ""}
                        onChange={(e) =>
                          setCfg({
                            ...cfg,
                            llm: {
                              ...cfg.llm,
                              roles: { ...cfg.llm.roles, subagent_model: e.target.value },
                            },
                          })
                        }
                      />
                    </div>
                  </div>
                  <datalist id="settings-models">
                    {models.map((m) => (
                      <option key={m.id} value={m.id} />
                    ))}
                  </datalist>

                  <div className="grid gap-4 md:grid-cols-3 pt-2 border-t border-border">
                    <div className="space-y-1.5">
                      <Label htmlFor="approval-mode">Tool Approval Mode</Label>
                      <select
                        id="approval-mode"
                        className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
                        value={cfg.tools.approval_mode}
                        onChange={(e) =>
                          setCfg({
                            ...cfg,
                            tools: { ...cfg.tools, approval_mode: e.target.value as ApprovalMode },
                          })
                        }
                      >
                        <option value="bypass">bypass</option>
                        <option value="auto-detection">auto-detection</option>
                        <option value="request">request</option>
                      </select>
                    </div>
                    <div className="space-y-1.5">
                      <Label htmlFor="browser-preferred">Preferred Browser Driver</Label>
                      <select
                        id="browser-preferred"
                        className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
                        value={cfg.tools.browser_control.preferred}
                        onChange={(e) =>
                          setCfg({
                            ...cfg,
                            tools: {
                              ...cfg.tools,
                              browser_control: {
                                ...cfg.tools.browser_control,
                                preferred: e.target.value as "cdp-q-driver" | "ego-browser",
                              },
                            },
                          })
                        }
                      >
                        <option value="cdp-q-driver">cdp-q-driver (built-in)</option>
                        <option value="ego-browser">ego-browser</option>
                      </select>
                    </div>
                    <div className="flex items-end gap-3 pb-2">
                      <Switch
                        id="browser-enabled"
                        checked={cfg.tools.browser_control.enabled}
                        onCheckedChange={(checked) =>
                          setCfg({
                            ...cfg,
                            tools: {
                              ...cfg.tools,
                              browser_control: { ...cfg.tools.browser_control, enabled: checked },
                            },
                          })
                        }
                      />
                      <Label htmlFor="browser-enabled">Browser Control Enabled</Label>
                    </div>
                  </div>

                  <div className="flex justify-end">
                    <Button onClick={() => void handleSaveStructured()}>
                      <Save className="h-4 w-4" />
                      Save Settings
                    </Button>
                  </div>
                </CardContent>
              </Card>
            </TabsContent>

            <TabsContent value="models">
              <Card>
                <CardHeader className="flex flex-row items-center justify-between">
                  <div>
                    <CardTitle>Provider Models &amp; Decisions</CardTitle>
                    <CardDescription>
                      Live model list cached in pkg/llm/gateway and Decisions registry.
                    </CardDescription>
                  </div>
                  <Button variant="outline" size="sm" onClick={() => void handleRefreshModels()}>
                    <RefreshCw className="h-4 w-4" />
                    Refresh Models
                  </Button>
                </CardHeader>
                <CardContent className="space-y-4">
                  <div className="space-y-1.5">
                    <div className="text-xs font-semibold text-muted-foreground">
                      Decisions Models ({decisionModels.length})
                    </div>
                    {decisionModels.length === 0 ? (
                      <div className="text-xs text-muted-foreground">No Decisions models configured.</div>
                    ) : (
                      <div className="flex flex-wrap gap-2">
                        {decisionModels.map((dm) => (
                          <Badge key={dm.id} variant="secondary">
                            {dm.id} ({dm.protocol})
                          </Badge>
                        ))}
                      </div>
                    )}
                  </div>
                  <div className="max-h-96 overflow-auto rounded-md border border-border divide-y divide-border text-xs">
                    {models.length === 0 ? (
                      <div className="p-4 text-muted-foreground">
                        No enabled LLM providers configured yet. Add providers in Raw config.json.
                      </div>
                    ) : (
                      models.map((m) => (
                        <div key={m.id} className="flex items-center justify-between px-3 py-2">
                          <span className="font-mono">{m.id}</span>
                          <div className="flex items-center gap-2 text-muted-foreground">
                            {m.context_length ? <span>ctx {m.context_length}</span> : null}
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

            <TabsContent value="json">
              <Card>
                <CardHeader>
                  <CardTitle>config.json Editor</CardTitle>
                  <CardDescription>
                    Edit providers, env-var references (*_env), Decisions, or embeddings directly. Secret values remain in environment variables.
                  </CardDescription>
                </CardHeader>
                <CardContent className="space-y-3">
                  <Textarea
                    className="min-h-[380px] font-mono text-xs"
                    value={rawJson}
                    onChange={(e) => setRawJson(e.target.value)}
                  />
                  <div className="flex justify-end">
                    <Button onClick={() => void handleSaveRawJson()}>
                      <Save className="h-4 w-4" />
                      Apply config.json
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
