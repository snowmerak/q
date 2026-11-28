import { useCallback, useEffect, useState } from "react";
import { BookOpen, Plus, Search, Sparkles, Trash2 } from "lucide-react";
import type { QClient } from "@/client/api";
import type { KnowledgeDoc, KnowledgeHit, SkillSummary } from "@/client/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";

export function KnowledgeView({ client }: { client: QClient }) {
  const [docs, setDocs] = useState<KnowledgeDoc[]>([]);
  const [skills, setSkills] = useState<SkillSummary[]>([]);
  const [hits, setHits] = useState<KnowledgeHit[]>([]);
  const [searchQuery, setSearchQuery] = useState("");
  const [title, setTitle] = useState("");
  const [scope, setScope] = useState("global");
  const [content, setContent] = useState("");
  const [statusMsg, setStatusMsg] = useState("");

  const loadAll = useCallback(async () => {
    try {
      const [dRes, sRes] = await Promise.all([client.listKnowledge(), client.listSkills()]);
      setDocs(dRes.items);
      setSkills(sRes.items);
    } catch (err) {
      setStatusMsg(err instanceof Error ? err.message : "Failed to load knowledge/skills");
    }
  }, [client]);

  useEffect(() => {
    void loadAll();
  }, [loadAll]);

  const handleCreateDoc = async () => {
    if (!title.trim() || !content.trim()) return;
    setStatusMsg("");
    try {
      await client.createKnowledge({
        scope: scope.trim() || "global",
        title: title.trim(),
        content: content.trim(),
      });
      setTitle("");
      setContent("");
      setStatusMsg("Document indexed.");
      await loadAll();
    } catch (err) {
      setStatusMsg(err instanceof Error ? err.message : "Create failed");
    }
  };

  const handleDeleteDoc = async (id: string) => {
    try {
      await client.deleteKnowledge(id);
      await loadAll();
    } catch (err) {
      setStatusMsg(err instanceof Error ? err.message : "Delete failed");
    }
  };

  const handleSearch = async () => {
    if (!searchQuery.trim()) return;
    setStatusMsg("");
    try {
      const res = await client.searchKnowledge({ query: searchQuery.trim(), mode: "hybrid", limit: 12 });
      setHits(res.hits);
    } catch (err) {
      setStatusMsg(err instanceof Error ? err.message : "Search failed");
    }
  };

  return (
    <div className="flex-1 overflow-auto p-6">
      <div className="mx-auto max-w-5xl space-y-5">
        <div className="flex items-center justify-between">
          <div>
            <h1 className="text-xl font-semibold tracking-tight">Knowledge &amp; Agent Skills</h1>
            <p className="text-sm text-muted-foreground">
              Manage reference documents chunked into hybrid search and inspect loaded SKILL.md modules.
            </p>
          </div>
          {statusMsg && <Badge variant="outline">{statusMsg}</Badge>}
        </div>

        <Tabs defaultValue="docs">
          <TabsList>
            <TabsTrigger value="docs">Knowledge Docs ({docs.length})</TabsTrigger>
            <TabsTrigger value="search">Search Test</TabsTrigger>
            <TabsTrigger value="skills">Agent Skills ({skills.length})</TabsTrigger>
          </TabsList>

          <TabsContent value="docs" className="space-y-5">
            <Card>
              <CardHeader>
                <CardTitle>Add Knowledge Document</CardTitle>
                <CardDescription>
                  Documents are automatically chunked and queued into index_outbox for BM25/vector search.
                </CardDescription>
              </CardHeader>
              <CardContent className="space-y-3">
                <div className="grid gap-3 md:grid-cols-2">
                  <div className="space-y-1">
                    <Label htmlFor="kd-title">Title</Label>
                    <Input
                      id="kd-title"
                      value={title}
                      onChange={(e) => setTitle(e.target.value)}
                      placeholder="World Lore / Ops Runbook"
                    />
                  </div>
                  <div className="space-y-1">
                    <Label htmlFor="kd-scope">Scope</Label>
                    <Input
                      id="kd-scope"
                      value={scope}
                      onChange={(e) => setScope(e.target.value)}
                      placeholder="global"
                    />
                  </div>
                </div>
                <div className="space-y-1">
                  <Label htmlFor="kd-content">Content</Label>
                  <Textarea
                    id="kd-content"
                    value={content}
                    onChange={(e) => setContent(e.target.value)}
                    placeholder="Paste markdown or reference notes…"
                  />
                </div>
                <div className="flex justify-end">
                  <Button disabled={!title.trim() || !content.trim()} onClick={() => void handleCreateDoc()}>
                    <Plus className="h-4 w-4" />
                    Add Document
                  </Button>
                </div>
              </CardContent>
            </Card>

            <div className="space-y-2.5">
              {docs.map((d) => (
                <Card key={d.id} className="p-4">
                  <div className="flex items-start justify-between gap-4">
                    <div className="space-y-1 min-w-0">
                      <div className="flex items-center gap-2">
                        <BookOpen className="h-4 w-4 text-primary shrink-0" />
                        <span className="font-semibold text-sm">{d.title}</span>
                        <Badge variant="secondary">{d.scope}</Badge>
                        <Badge variant="outline">rev {d.revision}</Badge>
                      </div>
                      <p className="text-xs text-muted-foreground line-clamp-2 whitespace-pre-wrap">
                        {d.content}
                      </p>
                    </div>
                    <Button size="icon" variant="ghost" onClick={() => void handleDeleteDoc(d.id)}>
                      <Trash2 className="h-4 w-4 text-destructive" />
                    </Button>
                  </div>
                </Card>
              ))}
            </div>
          </TabsContent>

          <TabsContent value="search" className="space-y-4">
            <div className="flex gap-2">
              <Input
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                placeholder="Query knowledge index…"
                onKeyDown={(e) => e.key === "Enter" && void handleSearch()}
              />
              <Button onClick={() => void handleSearch()}>
                <Search className="h-4 w-4" />
                Search
              </Button>
            </div>
            <div className="space-y-2.5">
              {hits.map((h) => (
                <Card key={h.chunk_id} className="p-4 text-xs space-y-1.5">
                  <div className="flex items-center justify-between">
                    <span className="font-semibold text-sm">{h.doc_title}</span>
                    <div className="flex gap-2">
                      <Badge variant="secondary">{h.scope}</Badge>
                      <Badge variant="outline">score {h.score.toFixed(3)}</Badge>
                    </div>
                  </div>
                  <div className="whitespace-pre-wrap text-muted-foreground">{h.text}</div>
                </Card>
              ))}
            </div>
          </TabsContent>

          <TabsContent value="skills" className="space-y-3">
            {skills.length === 0 ? (
              <Card className="p-6 text-sm text-muted-foreground">
                No Agent Skills found in <code>&lt;data-dir&gt;/skills/*/SKILL.md</code>.
              </Card>
            ) : (
              skills.map((s) => (
                <Card key={s.name} className="p-4">
                  <div className="flex items-center gap-2 font-semibold text-sm">
                    <Sparkles className="h-4 w-4 text-primary" />
                    {s.name}
                  </div>
                  <p className="mt-1 text-xs text-muted-foreground">{s.description}</p>
                  <div className="mt-1 text-[11px] font-mono text-muted-foreground">{s.path}</div>
                </Card>
              ))
            )}
          </TabsContent>
        </Tabs>
      </div>
    </div>
  );
}
