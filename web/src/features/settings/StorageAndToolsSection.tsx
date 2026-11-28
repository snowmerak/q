import { useState } from "react";
import { Database, Plus, Trash2 } from "lucide-react";
import type { ApprovalMode, ConfigView, MCPServerConfig, ModelInfo } from "@/client/types";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ModelSelect } from "@/components/ui/model-select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";

function parseIntOpt(raw: string): number | undefined {
  const trimmed = raw.trim();
  if (!trimmed) return undefined;
  const parsed = Number(trimmed);
  return Number.isFinite(parsed) ? Math.trunc(parsed) : undefined;
}

const EMBEDDING_CAPABLE_PROVIDER_TYPES = new Set([
  "openai-compatible",
  "openrouter",
  "grok",
  "xai",
]);

function stripProviderPrefix(modelId: string, prefix?: string): string {
  const cleanPrefix = (prefix ?? "").trim();
  if (cleanPrefix && modelId.startsWith(`${cleanPrefix}/`)) {
    return modelId.slice(cleanPrefix.length + 1);
  }
  return modelId;
}

export function EmbeddingsSection({
  cfg,
  models,
  onChange,
  onReindex,
}: {
  cfg: ConfigView;
  models: ModelInfo[];
  onChange: (next: ConfigView) => void;
  onReindex: () => void;
}) {
  const emb = cfg.embeddings;
  const updateEmb = (patch: Partial<ConfigView["embeddings"]>) =>
    onChange({ ...cfg, embeddings: { ...emb, ...patch } });

  const embeddingProviders = (cfg.llm.providers ?? []).filter((p) =>
    EMBEDDING_CAPABLE_PROVIDER_TYPES.has(p.type),
  );
  const selectedProviderId = emb.provider_id ?? "";
  const selectedProvider = embeddingProviders.find((p) => p.id === selectedProviderId);
  const usingConfiguredProvider = selectedProviderId !== "";

  const providerPrefix = selectedProvider?.prefix?.trim() ?? "";
  const providerModels = usingConfiguredProvider
    ? models
        .filter((m) =>
          providerPrefix ? m.id.startsWith(`${providerPrefix}/`) : true,
        )
        .map((m) => {
          const stripped = stripProviderPrefix(m.id, providerPrefix);
          return {
            id: stripped,
            label: stripped !== m.id ? `${stripped} (${m.id})` : m.id,
          };
        })
    : [];

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between gap-4">
        <div>
          <CardTitle>Vector Embeddings</CardTitle>
          <CardDescription>
            Reuse a configured OpenAI-compatible LLM provider or point to a standalone <code>/embeddings</code> endpoint for hybrid vector recall and knowledge search.
          </CardDescription>
        </div>
        <Button type="button" variant="outline" size="sm" onClick={onReindex}>
          <Database className="h-4 w-4" />
          Reindex Search
        </Button>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex items-center gap-3">
          <Switch
            id="emb-enabled"
            checked={emb.enabled}
            onCheckedChange={(checked) => updateEmb({ enabled: checked })}
          />
          <Label htmlFor="emb-enabled">Enable Vector Embeddings</Label>
        </div>

        <div className="grid gap-4 md:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor="emb-provider">Provider</Label>
            <select
              id="emb-provider"
              className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
              value={selectedProviderId}
              onChange={(e) => {
                const nextPid = e.target.value;
                if (nextPid) {
                  updateEmb({
                    provider_id: nextPid,
                    base_url: undefined,
                    api_key_env: undefined,
                  });
                } else {
                  updateEmb({ provider_id: undefined });
                }
              }}
            >
              {embeddingProviders.map((p, idx) => (
                <option key={idx} value={p.id}>
                  {p.id} ({p.type}
                  {p.enabled ? "" : " — disabled"})
                </option>
              ))}
              {usingConfiguredProvider && !selectedProvider && (
                <option value={selectedProviderId}>
                  {selectedProviderId} (not in Providers)
                </option>
              )}
              <option value="">Custom standalone endpoint (Base URL)</option>
            </select>
            {usingConfiguredProvider && (
              <p className="text-xs text-muted-foreground">
                Base URL, API key, and custom headers are inherited from the selected LLM provider.
              </p>
            )}
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="emb-model">Embedding Model</Label>
            {usingConfiguredProvider ? (
              <ModelSelect
                id="emb-model"
                value={emb.model ?? ""}
                options={providerModels}
                emptyLabel="— Select embedding model —"
                customPlaceholder="text-embedding-3-small"
                onChange={(v) => updateEmb({ model: v || undefined })}
              />
            ) : (
              <Input
                id="emb-model"
                value={emb.model ?? ""}
                onChange={(e) => updateEmb({ model: e.target.value || undefined })}
                placeholder="text-embedding-3-small"
              />
            )}
          </div>
        </div>

        {!usingConfiguredProvider && (
          <div className="grid gap-4 md:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="emb-url">Base URL</Label>
              <Input
                id="emb-url"
                value={emb.base_url ?? ""}
                onChange={(e) => updateEmb({ base_url: e.target.value || undefined })}
                placeholder="https://api.openai.com/v1"
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="emb-key-env">API Key Env Var</Label>
              <Input
                id="emb-key-env"
                value={emb.api_key_env ?? ""}
                onChange={(e) => updateEmb({ api_key_env: e.target.value || undefined })}
                placeholder="OPENAI_API_KEY"
              />
            </div>
          </div>
        )}

        <div className="grid gap-4 md:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor="emb-rev">Revision Tag</Label>
            <Input
              id="emb-rev"
              value={emb.revision ?? ""}
              onChange={(e) => updateEmb({ revision: e.target.value || undefined })}
              placeholder="v1"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="emb-dim">Dimensions</Label>
            <Input
              id="emb-dim"
              type="number"
              min={0}
              value={emb.dimensions ?? ""}
              onChange={(e) => updateEmb({ dimensions: parseIntOpt(e.target.value) })}
              placeholder="1536 (0 for provider default)"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="emb-timeout">Timeout (ms)</Label>
            <Input
              id="emb-timeout"
              type="number"
              min={0}
              value={emb.timeout_ms ?? ""}
              onChange={(e) => updateEmb({ timeout_ms: parseIntOpt(e.target.value) })}
              placeholder="15000"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="emb-q-prefix">Query Prefix</Label>
            <Input
              id="emb-q-prefix"
              value={emb.query_prefix ?? ""}
              onChange={(e) => updateEmb({ query_prefix: e.target.value || undefined })}
              placeholder="Optional, e.g. 'query: '"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="emb-d-prefix">Document Prefix</Label>
            <Input
              id="emb-d-prefix"
              value={emb.document_prefix ?? ""}
              onChange={(e) => updateEmb({ document_prefix: e.target.value || undefined })}
              placeholder="Optional, e.g. 'passage: '"
            />
          </div>
        </div>
      </CardContent>
    </Card>
  );
}

export function ToolsAndMCPSection({
  cfg,
  onChange,
}: {
  cfg: ConfigView;
  onChange: (next: ConfigView) => void;
}) {
  const [newToolName, setNewToolName] = useState("");
  const [newToolMode, setNewToolMode] = useState<ApprovalMode>("request");

  const tools = cfg.tools;
  const overrides = tools.tool_overrides ?? {};
  const drivers = tools.browser_control.drivers ?? {};

  const updateTools = (patch: Partial<ConfigView["tools"]>) =>
    onChange({ ...cfg, tools: { ...tools, ...patch } });

  const updateDriver = (name: "cdp-q-driver" | "ego-browser", patch: { command?: string; args?: string[] }) => {
    const cur = drivers[name] ?? {};
    updateTools({
      browser_control: {
        ...tools.browser_control,
        drivers: { ...drivers, [name]: { ...cur, ...patch } },
      },
    });
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle>Tools, Browser Control &amp; MCP</CardTitle>
        <CardDescription>
          Manage approval gate policies, workspace root guards, browser automation drivers, and external MCP servers.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-5">
        <div className="grid gap-4 md:grid-cols-3">
          <div className="space-y-1.5">
            <Label htmlFor="approval-mode">Tool Approval Mode</Label>
            <select
              id="approval-mode"
              className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
              value={tools.approval_mode}
              onChange={(e) => updateTools({ approval_mode: e.target.value as ApprovalMode })}
            >
              <option value="bypass">bypass</option>
              <option value="auto-detection">auto-detection</option>
              <option value="request">request</option>
            </select>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="tools-timeout">Command Timeout (ms)</Label>
            <Input
              id="tools-timeout"
              type="number"
              min={0}
              value={tools.command_timeout_ms ?? ""}
              onChange={(e) => updateTools({ command_timeout_ms: parseIntOpt(e.target.value) })}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="tools-depth">Max Delegation Depth</Label>
            <Input
              id="tools-depth"
              type="number"
              min={0}
              value={tools.max_delegation_depth ?? ""}
              onChange={(e) => updateTools({ max_delegation_depth: parseIntOpt(e.target.value) })}
            />
          </div>
        </div>

        <div className="space-y-1.5">
          <Label htmlFor="tools-roots">Allowed File Roots (one path per line)</Label>
          <Textarea
            id="tools-roots"
            className="min-h-[76px] font-mono text-xs"
            value={(tools.allowed_roots ?? []).join("\n")}
            onChange={(e) => {
              const roots = e.target.value
                .split("\n")
                .map((s) => s.trim())
                .filter(Boolean);
              updateTools({ allowed_roots: roots.length > 0 ? roots : undefined });
            }}
            placeholder="Leave empty to restrict writes safely or list allowed directories"
          />
        </div>

        {/* Per-tool approval overrides */}
        <div className="space-y-2.5 pt-2 border-t border-border">
          <Label>Per-Tool Approval Overrides</Label>
          <div className="flex flex-wrap gap-2">
            <Input
              className="w-52"
              placeholder="Tool name (e.g. read_file)"
              value={newToolName}
              onChange={(e) => setNewToolName(e.target.value)}
            />
            <select
              className="flex h-9 rounded-md border border-input bg-background px-3 text-sm"
              value={newToolMode}
              onChange={(e) => setNewToolMode(e.target.value as ApprovalMode)}
            >
              <option value="bypass">bypass</option>
              <option value="auto-detection">auto-detection</option>
              <option value="request">request</option>
            </select>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => {
                const name = newToolName.trim();
                if (!name) return;
                updateTools({ tool_overrides: { ...overrides, [name]: newToolMode } });
                setNewToolName("");
              }}
            >
              <Plus className="h-3.5 w-3.5" />
              Set Override
            </Button>
          </div>
          {Object.entries(overrides).length > 0 && (
            <div className="grid gap-2 md:grid-cols-2">
              {Object.entries(overrides).map(([name, mode]) => (
                <div
                  key={name}
                  className="flex items-center justify-between gap-2 rounded-md border border-border px-3 py-1.5 text-xs bg-background/50"
                >
                  <span className="font-mono">{name}</span>
                  <div className="flex items-center gap-1.5">
                    <select
                      className="h-7 rounded border border-input bg-background px-2 text-xs"
                      value={mode}
                      onChange={(e) =>
                        updateTools({
                          tool_overrides: { ...overrides, [name]: e.target.value as ApprovalMode },
                        })
                      }
                    >
                      <option value="bypass">bypass</option>
                      <option value="auto-detection">auto-detection</option>
                      <option value="request">request</option>
                    </select>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      onClick={() => {
                        const next = { ...overrides };
                        delete next[name];
                        updateTools({ tool_overrides: Object.keys(next).length ? next : undefined });
                      }}
                    >
                      <Trash2 className="h-3.5 w-3.5 text-destructive" />
                    </Button>
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>

        {/* Browser control */}
        <div className="space-y-3 pt-2 border-t border-border">
          <div className="grid gap-4 md:grid-cols-2 items-end">
            <div className="flex items-center gap-3 pb-2">
              <Switch
                id="browser-enabled"
                checked={tools.browser_control.enabled}
                onCheckedChange={(checked) =>
                  updateTools({
                    browser_control: { ...tools.browser_control, enabled: checked },
                  })
                }
              />
              <Label htmlFor="browser-enabled">Browser Control Enabled</Label>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="browser-preferred">Preferred Browser Driver</Label>
              <select
                id="browser-preferred"
                className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
                value={tools.browser_control.preferred}
                onChange={(e) =>
                  updateTools({
                    browser_control: {
                      ...tools.browser_control,
                      preferred: e.target.value as "cdp-q-driver" | "ego-browser",
                    },
                  })
                }
              >
                <option value="cdp-q-driver">cdp-q-driver (built-in)</option>
                <option value="ego-browser">ego-browser</option>
              </select>
            </div>
          </div>
          <div className="grid gap-3 md:grid-cols-2">
            {(["cdp-q-driver", "ego-browser"] as const).map((drv) => (
              <div key={drv} className="rounded-md border border-border p-3 space-y-2 bg-background/50">
                <div className="text-xs font-semibold font-mono">{drv}</div>
                <Input
                  value={drivers[drv]?.command ?? ""}
                  onChange={(e) =>
                    updateDriver(drv, { command: e.target.value || undefined })
                  }
                  placeholder={drv === "cdp-q-driver" ? "(built-in default if empty)" : "ego-browser command"}
                />
                <Input
                  value={(drivers[drv]?.args ?? []).join(", ")}
                  onChange={(e) => {
                    const args = e.target.value
                      .split(",")
                      .map((s) => s.trim())
                      .filter(Boolean);
                    updateDriver(drv, { args: args.length > 0 ? args : undefined });
                  }}
                  placeholder="Optional args (comma-separated)"
                />
              </div>
            ))}
          </div>
        </div>

        {/* MCP Servers */}
        <div className="space-y-3 pt-2 border-t border-border">
          <div className="flex items-center justify-between">
            <div>
              <div className="text-sm font-medium">MCP Servers</div>
              <p className="text-xs text-muted-foreground">
                Configure external Model Context Protocol stdio or HTTP tool servers.
              </p>
            </div>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => {
                const existing = tools.mcp_servers ?? [];
                const next: MCPServerConfig = {
                  id: `mcp-${existing.length + 1}`,
                  transport: "stdio",
                  enabled: true,
                  command: "",
                };
                updateTools({ mcp_servers: [...existing, next] });
              }}
            >
              <Plus className="h-3.5 w-3.5" />
              Add Server
            </Button>
          </div>
          {(tools.mcp_servers ?? []).length === 0 ? (
            <div className="rounded-md border border-dashed border-border p-3 text-xs text-muted-foreground">
              No MCP servers configured.
            </div>
          ) : (
            <div className="space-y-2.5">
              {(tools.mcp_servers ?? []).map((srv, idx) => {
                const updateSrv = (patch: Partial<MCPServerConfig>) => {
                  const nextList = [...(tools.mcp_servers ?? [])];
                  nextList[idx] = { ...srv, ...patch };
                  updateTools({ mcp_servers: nextList });
                };
                return (
                  <div
                    key={idx}
                    className="rounded-md border border-border p-3 space-y-2.5 bg-background/50"
                  >
                    <div className="grid gap-2 md:grid-cols-4 items-end">
                      <div className="space-y-1">
                        <Label className="text-xs">Server ID</Label>
                        <Input
                          value={srv.id}
                          onChange={(e) => updateSrv({ id: e.target.value })}
                          placeholder="filesystem"
                        />
                      </div>
                      <div className="space-y-1">
                        <Label className="text-xs">Transport</Label>
                        <select
                          className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
                          value={srv.transport}
                          onChange={(e) =>
                            updateSrv({ transport: e.target.value as "stdio" | "http" })
                          }
                        >
                          <option value="stdio">stdio</option>
                          <option value="http">http</option>
                        </select>
                      </div>
                      <div className="flex items-center gap-2 pb-2">
                        <Switch
                          checked={srv.enabled}
                          onCheckedChange={(checked) => updateSrv({ enabled: checked })}
                        />
                        <span className="text-xs">Enabled</span>
                      </div>
                      <div className="flex justify-end pb-1">
                        <Button
                          type="button"
                          variant="ghost"
                          size="sm"
                          onClick={() =>
                            updateTools({
                              mcp_servers: (tools.mcp_servers ?? []).filter((_, i) => i !== idx),
                            })
                          }
                        >
                          <Trash2 className="h-3.5 w-3.5 text-destructive" />
                        </Button>
                      </div>
                    </div>
                    {srv.transport === "http" ? (
                      <div className="space-y-1">
                        <Label className="text-xs">Endpoint URL</Label>
                        <Input
                          value={srv.url ?? ""}
                          onChange={(e) => updateSrv({ url: e.target.value || undefined })}
                          placeholder="http://127.0.0.1:8090/mcp"
                        />
                      </div>
                    ) : (
                      <div className="grid gap-2 md:grid-cols-2">
                        <div className="space-y-1">
                          <Label className="text-xs">Command</Label>
                          <Input
                            value={srv.command ?? ""}
                            onChange={(e) => updateSrv({ command: e.target.value || undefined })}
                            placeholder="npx"
                          />
                        </div>
                        <div className="space-y-1">
                          <Label className="text-xs">Args (comma-separated)</Label>
                          <Input
                            value={(srv.args ?? []).join(", ")}
                            onChange={(e) => {
                              const args = e.target.value
                                .split(",")
                                .map((s) => s.trim())
                                .filter(Boolean);
                              updateSrv({ args: args.length > 0 ? args : undefined });
                            }}
                            placeholder="-y, @modelcontextprotocol/server-everything"
                          />
                        </div>
                      </div>
                    )}
                  </div>
                );
              })}
            </div>
          )}
        </div>
      </CardContent>
    </Card>
  );
}

export function StorageAndServerSection({
  cfg,
  onChange,
}: {
  cfg: ConfigView;
  onChange: (next: ConfigView) => void;
}) {
  const { server, storage } = cfg;

  return (
    <div className="space-y-5">
      <Card>
        <CardHeader>
          <CardTitle>HTTP Server</CardTitle>
          <CardDescription>
            Server bind address and CORS settings. Changes here usually require a restart.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid gap-4 md:grid-cols-2 items-end">
            <div className="space-y-1.5">
              <Label htmlFor="srv-listen">Listen Address</Label>
              <Input
                id="srv-listen"
                value={server.listen}
                onChange={(e) =>
                  onChange({ ...cfg, server: { ...server, listen: e.target.value } })
                }
                placeholder="127.0.0.1:17070"
              />
            </div>
            <div className="flex items-center gap-3 pb-2">
              <Switch
                id="srv-browser"
                checked={server.open_browser}
                onCheckedChange={(checked) =>
                  onChange({ ...cfg, server: { ...server, open_browser: checked } })
                }
              />
              <Label htmlFor="srv-browser">Open Browser on CLI Start</Label>
            </div>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="srv-cors">CORS Allowed Origins (one per line)</Label>
            <Textarea
              id="srv-cors"
              className="min-h-[70px] font-mono text-xs"
              value={(server.cors_origins ?? []).join("\n")}
              onChange={(e) => {
                const list = e.target.value
                  .split("\n")
                  .map((s) => s.trim())
                  .filter(Boolean);
                onChange({
                  ...cfg,
                  server: { ...server, cors_origins: list.length > 0 ? list : undefined },
                });
              }}
              placeholder="http://localhost:5173"
            />
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Composable Storage Backends</CardTitle>
          <CardDescription>
            Select RDB authority, Search projection, and Blob storage drivers. Storage driver changes require restarting <code>q</code>.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-5">
          {/* RDB */}
          <div className="grid gap-4 md:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="rdb-driver">RDB Driver (Authority)</Label>
              <select
                id="rdb-driver"
                className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
                value={storage.rdb.driver}
                onChange={(e) =>
                  onChange({
                    ...cfg,
                    storage: { ...storage, rdb: { ...storage.rdb, driver: e.target.value } },
                  })
                }
              >
                <option value="sqlite">sqlite (local)</option>
                <option value="postgres">postgres</option>
                <option value="mysql">mysql</option>
              </select>
            </div>
            {storage.rdb.driver === "sqlite" ? (
              <div className="space-y-1.5">
                <Label htmlFor="rdb-sqlite-path">SQLite File Path</Label>
                <Input
                  id="rdb-sqlite-path"
                  value={storage.rdb.sqlite_path ?? ""}
                  onChange={(e) =>
                    onChange({
                      ...cfg,
                      storage: {
                        ...storage,
                        rdb: { ...storage.rdb, sqlite_path: e.target.value || undefined },
                      },
                    })
                  }
                  placeholder="q.sqlite"
                />
              </div>
            ) : (
              <div className="space-y-1.5">
                <Label htmlFor="rdb-dsn-env">DSN Env Var</Label>
                <Input
                  id="rdb-dsn-env"
                  value={storage.rdb.dsn_env ?? ""}
                  onChange={(e) =>
                    onChange({
                      ...cfg,
                      storage: {
                        ...storage,
                        rdb: { ...storage.rdb, dsn_env: e.target.value || undefined },
                      },
                    })
                  }
                  placeholder="Q_RDB_DSN"
                />
              </div>
            )}
          </div>

          {/* Search */}
          <div className="space-y-3 pt-2 border-t border-border">
            <div className="grid gap-4 md:grid-cols-2">
              <div className="space-y-1.5">
                <Label htmlFor="search-driver">Search Driver (Projection)</Label>
                <select
                  id="search-driver"
                  className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
                  value={storage.search.driver}
                  onChange={(e) =>
                    onChange({
                      ...cfg,
                      storage: {
                        ...storage,
                        search: { ...storage.search, driver: e.target.value },
                      },
                    })
                  }
                >
                  <option value="bleve">bleve (embedded Bleve + HNSW)</option>
                  <option value="opensearch">opensearch</option>
                </select>
              </div>
            </div>
            {storage.search.driver === "opensearch" && (
              <div className="grid gap-3 md:grid-cols-3 rounded-md border border-border p-3 bg-background/50">
                <div className="space-y-1">
                  <Label className="text-xs">Endpoint</Label>
                  <Input
                    value={storage.search.opensearch?.endpoint ?? ""}
                    onChange={(e) =>
                      onChange({
                        ...cfg,
                        storage: {
                          ...storage,
                          search: {
                            ...storage.search,
                            opensearch: {
                              ...storage.search.opensearch,
                              endpoint: e.target.value || undefined,
                            },
                          },
                        },
                      })
                    }
                    placeholder="http://127.0.0.1:9200"
                  />
                </div>
                <div className="space-y-1">
                  <Label className="text-xs">Index Prefix</Label>
                  <Input
                    value={storage.search.opensearch?.index_prefix ?? ""}
                    onChange={(e) =>
                      onChange({
                        ...cfg,
                        storage: {
                          ...storage,
                          search: {
                            ...storage.search,
                            opensearch: {
                              ...storage.search.opensearch,
                              index_prefix: e.target.value || undefined,
                            },
                          },
                        },
                      })
                    }
                    placeholder="q"
                  />
                </div>
                <div className="space-y-1">
                  <Label className="text-xs">Timeout (ms)</Label>
                  <Input
                    type="number"
                    min={0}
                    value={storage.search.opensearch?.timeout_ms ?? ""}
                    onChange={(e) =>
                      onChange({
                        ...cfg,
                        storage: {
                          ...storage,
                          search: {
                            ...storage.search,
                            opensearch: {
                              ...storage.search.opensearch,
                              timeout_ms: parseIntOpt(e.target.value),
                            },
                          },
                        },
                      })
                    }
                  />
                </div>
                <div className="space-y-1">
                  <Label className="text-xs">Username</Label>
                  <Input
                    value={storage.search.opensearch?.username ?? ""}
                    onChange={(e) =>
                      onChange({
                        ...cfg,
                        storage: {
                          ...storage,
                          search: {
                            ...storage.search,
                            opensearch: {
                              ...storage.search.opensearch,
                              username: e.target.value || undefined,
                            },
                          },
                        },
                      })
                    }
                  />
                </div>
                <div className="space-y-1">
                  <Label className="text-xs">Password Env Var</Label>
                  <Input
                    value={storage.search.opensearch?.password_env ?? ""}
                    onChange={(e) =>
                      onChange({
                        ...cfg,
                        storage: {
                          ...storage,
                          search: {
                            ...storage.search,
                            opensearch: {
                              ...storage.search.opensearch,
                              password_env: e.target.value || undefined,
                            },
                          },
                        },
                      })
                    }
                  />
                </div>
                <div className="space-y-1">
                  <Label className="text-xs">Bearer Token Env Var</Label>
                  <Input
                    value={storage.search.opensearch?.bearer_token_env ?? ""}
                    onChange={(e) =>
                      onChange({
                        ...cfg,
                        storage: {
                          ...storage,
                          search: {
                            ...storage.search,
                            opensearch: {
                              ...storage.search.opensearch,
                              bearer_token_env: e.target.value || undefined,
                            },
                          },
                        },
                      })
                    }
                  />
                </div>
              </div>
            )}
          </div>

          {/* Blob */}
          <div className="space-y-3 pt-2 border-t border-border">
            <div className="grid gap-3 md:grid-cols-4">
              <div className="space-y-1.5">
                <Label htmlFor="blob-driver">Blob Driver</Label>
                <select
                  id="blob-driver"
                  className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
                  value={storage.blob.driver}
                  onChange={(e) =>
                    onChange({
                      ...cfg,
                      storage: { ...storage, blob: { ...storage.blob, driver: e.target.value } },
                    })
                  }
                >
                  <option value="local">local</option>
                  <option value="s3">s3</option>
                </select>
              </div>
              <div className="space-y-1.5">
                <Label className="text-xs">Max Upload Bytes</Label>
                <Input
                  type="number"
                  min={0}
                  value={storage.blob.max_upload_bytes ?? ""}
                  onChange={(e) =>
                    onChange({
                      ...cfg,
                      storage: {
                        ...storage,
                        blob: { ...storage.blob, max_upload_bytes: parseIntOpt(e.target.value) },
                      },
                    })
                  }
                />
              </div>
              <div className="space-y-1.5">
                <Label className="text-xs">Loom Max Artifact Bytes</Label>
                <Input
                  type="number"
                  min={0}
                  value={storage.blob.loom_max_artifact_bytes ?? ""}
                  onChange={(e) =>
                    onChange({
                      ...cfg,
                      storage: {
                        ...storage,
                        blob: {
                          ...storage.blob,
                          loom_max_artifact_bytes: parseIntOpt(e.target.value),
                        },
                      },
                    })
                  }
                />
              </div>
              <div className="space-y-1.5">
                <Label className="text-xs">Loom Max Store Bytes</Label>
                <Input
                  type="number"
                  min={0}
                  value={storage.blob.loom_max_store_bytes ?? ""}
                  onChange={(e) =>
                    onChange({
                      ...cfg,
                      storage: {
                        ...storage,
                        blob: {
                          ...storage.blob,
                          loom_max_store_bytes: parseIntOpt(e.target.value),
                        },
                      },
                    })
                  }
                />
              </div>
            </div>

            {storage.blob.driver === "s3" && (
              <div className="grid gap-3 md:grid-cols-3 rounded-md border border-border p-3 bg-background/50">
                <div className="space-y-1">
                  <Label className="text-xs">S3 Endpoint (optional)</Label>
                  <Input
                    value={storage.blob.s3?.endpoint ?? ""}
                    onChange={(e) =>
                      onChange({
                        ...cfg,
                        storage: {
                          ...storage,
                          blob: {
                            ...storage.blob,
                            s3: { ...storage.blob.s3, endpoint: e.target.value || undefined },
                          },
                        },
                      })
                    }
                    placeholder="http://127.0.0.1:9000"
                  />
                </div>
                <div className="space-y-1">
                  <Label className="text-xs">Bucket</Label>
                  <Input
                    value={storage.blob.s3?.bucket ?? ""}
                    onChange={(e) =>
                      onChange({
                        ...cfg,
                        storage: {
                          ...storage,
                          blob: {
                            ...storage.blob,
                            s3: { ...storage.blob.s3, bucket: e.target.value || undefined },
                          },
                        },
                      })
                    }
                  />
                </div>
                <div className="space-y-1">
                  <Label className="text-xs">Region</Label>
                  <Input
                    value={storage.blob.s3?.region ?? ""}
                    onChange={(e) =>
                      onChange({
                        ...cfg,
                        storage: {
                          ...storage,
                          blob: {
                            ...storage.blob,
                            s3: { ...storage.blob.s3, region: e.target.value || undefined },
                          },
                        },
                      })
                    }
                    placeholder="us-east-1"
                  />
                </div>
                <div className="space-y-1">
                  <Label className="text-xs">Access Key Env Var</Label>
                  <Input
                    value={storage.blob.s3?.access_key_env ?? ""}
                    onChange={(e) =>
                      onChange({
                        ...cfg,
                        storage: {
                          ...storage,
                          blob: {
                            ...storage.blob,
                            s3: { ...storage.blob.s3, access_key_env: e.target.value || undefined },
                          },
                        },
                      })
                    }
                  />
                </div>
                <div className="space-y-1">
                  <Label className="text-xs">Secret Key Env Var</Label>
                  <Input
                    value={storage.blob.s3?.secret_key_env ?? ""}
                    onChange={(e) =>
                      onChange({
                        ...cfg,
                        storage: {
                          ...storage,
                          blob: {
                            ...storage.blob,
                            s3: { ...storage.blob.s3, secret_key_env: e.target.value || undefined },
                          },
                        },
                      })
                    }
                  />
                </div>
              </div>
            )}
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
