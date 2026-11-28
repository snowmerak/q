import { useCallback, useEffect, useState } from "react";
import { Plus, Save, Trash2 } from "lucide-react";
import type { QClient } from "@/client/api";
import { QAPIError } from "@/client/api";
import type { ApprovalMode, Character, ModelInfo, Persona } from "@/client/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";

export function CharactersView({ client }: { client: QClient }) {
  const [characters, setCharacters] = useState<Character[]>([]);
  const [personas, setPersonas] = useState<Persona[]>([]);
  const [models, setModels] = useState<ModelInfo[]>([]);
  const [selectedCharId, setSelectedCharId] = useState<string>("__new__");
  const [selectedPersonaId, setSelectedPersonaId] = useState<string>("__new__");
  const [banner, setBanner] = useState<string>("");

  // Character form
  const [charName, setCharName] = useState("");
  const [personality, setPersonality] = useState("");
  const [background, setBackground] = useState("");
  const [appearance, setAppearance] = useState("");
  const [initialValuesText, setInitialValuesText] = useState("");
  const [chatModel, setChatModel] = useState("");
  const [memoryModel, setMemoryModel] = useState("");
  const [approvalOverride, setApprovalOverride] = useState<"" | ApprovalMode>("");
  const [charVersion, setCharVersion] = useState<number>(1);

  // Persona form
  const [personaName, setPersonaName] = useState("");
  const [personaDesc, setPersonaDesc] = useState("");
  const [personaDefault, setPersonaDefault] = useState(false);
  const [personaVersion, setPersonaVersion] = useState<number>(1);

  const loadAll = useCallback(async () => {
    try {
      const [cRes, pRes, mRes] = await Promise.all([
        client.listCharacters(),
        client.listPersonas(),
        client.listModels(),
      ]);
      setCharacters(cRes.items);
      setPersonas(pRes.items);
      setModels(mRes.models);
    } catch (err) {
      setBanner(err instanceof Error ? err.message : "Failed to load catalog");
    }
  }, [client]);

  useEffect(() => {
    void loadAll();
  }, [loadAll]);

  const selectCharacter = (c: Character | null) => {
    setBanner("");
    if (!c) {
      setSelectedCharId("__new__");
      setCharName("");
      setPersonality("");
      setBackground("");
      setAppearance("");
      setInitialValuesText("");
      setChatModel("");
      setMemoryModel("");
      setApprovalOverride("");
      setCharVersion(1);
      return;
    }
    setSelectedCharId(c.id);
    setCharName(c.name);
    setPersonality(c.personality ?? "");
    setBackground(c.background ?? "");
    setAppearance(c.appearance ?? "");
    setInitialValuesText((c.initial_values ?? []).join("\n"));
    setChatModel(c.model_prefs?.chat_model ?? "");
    setMemoryModel(c.model_prefs?.memory_model ?? "");
    setApprovalOverride(c.approval_policy_override ?? "");
    setCharVersion(c.version);
  };

  const selectPersona = (p: Persona | null) => {
    setBanner("");
    if (!p) {
      setSelectedPersonaId("__new__");
      setPersonaName("");
      setPersonaDesc("");
      setPersonaDefault(false);
      setPersonaVersion(1);
      return;
    }
    setSelectedPersonaId(p.id);
    setPersonaName(p.name);
    setPersonaDesc(p.description ?? "");
    setPersonaDefault(p.is_default);
    setPersonaVersion(p.version);
  };

  const saveCharacter = async () => {
    if (!charName.trim()) {
      setBanner("Character name is required.");
      return;
    }
    setBanner("");
    const values = initialValuesText
      .split("\n")
      .map((s) => s.trim())
      .filter(Boolean);
    const payload = {
      name: charName.trim(),
      personality,
      background,
      appearance,
      initial_values: values,
      model_prefs:
        chatModel || memoryModel
          ? {
              ...(chatModel ? { chat_model: chatModel } : {}),
              ...(memoryModel ? { memory_model: memoryModel } : {}),
            }
          : undefined,
      ...(approvalOverride ? { approval_policy_override: approvalOverride } : {}),
    };
    try {
      if (selectedCharId === "__new__") {
        const created = await client.createCharacter(payload);
        await loadAll();
        selectCharacter(created);
      } else {
        const updated = await client.patchCharacter(selectedCharId, {
          ...payload,
          expected_version: charVersion,
        });
        await loadAll();
        selectCharacter(updated);
      }
      setBanner("Saved character.");
    } catch (err) {
      setBanner(err instanceof QAPIError ? `${err.code}: ${err.message}` : "Failed to save character");
    }
  };

  const deleteCharacter = async () => {
    if (selectedCharId === "__new__") return;
    try {
      await client.deleteCharacter(selectedCharId);
      await loadAll();
      selectCharacter(null);
    } catch (err) {
      setBanner(err instanceof Error ? err.message : "Failed to delete character");
    }
  };

  const savePersona = async () => {
    if (!personaName.trim()) {
      setBanner("Persona name is required.");
      return;
    }
    setBanner("");
    try {
      if (selectedPersonaId === "__new__") {
        const created = await client.createPersona({
          name: personaName.trim(),
          description: personaDesc,
          is_default: personaDefault,
        });
        await loadAll();
        selectPersona(created);
      } else {
        const updated = await client.patchPersona(selectedPersonaId, {
          expected_version: personaVersion,
          name: personaName.trim(),
          description: personaDesc,
          is_default: personaDefault,
        });
        await loadAll();
        selectPersona(updated);
      }
      setBanner("Saved persona.");
    } catch (err) {
      setBanner(err instanceof QAPIError ? `${err.code}: ${err.message}` : "Failed to save persona");
    }
  };

  const deletePersona = async () => {
    if (selectedPersonaId === "__new__") return;
    try {
      await client.deletePersona(selectedPersonaId);
      await loadAll();
      selectPersona(null);
    } catch (err) {
      setBanner(err instanceof Error ? err.message : "Failed to delete persona");
    }
  };

  return (
    <div className="flex-1 overflow-auto p-6">
      <div className="mx-auto max-w-5xl space-y-4">
        <div className="flex items-center justify-between">
          <div>
            <h1 className="text-xl font-semibold tracking-tight">Characters &amp; Personas</h1>
            <p className="text-sm text-muted-foreground">
              Author character personalities, initial values, model preferences, and your user personas.
            </p>
          </div>
          {banner && <Badge variant="outline">{banner}</Badge>}
        </div>

        <Tabs defaultValue="characters">
          <TabsList>
            <TabsTrigger value="characters">Characters ({characters.length})</TabsTrigger>
            <TabsTrigger value="personas">User Personas ({personas.length})</TabsTrigger>
          </TabsList>

          <TabsContent value="characters">
            <div className="grid grid-cols-1 gap-5 md:grid-cols-[240px_1fr]">
              <Card className="p-2 h-fit">
                <Button
                  size="sm"
                  className="mb-2 w-full"
                  variant="outline"
                  onClick={() => selectCharacter(null)}
                >
                  <Plus className="h-4 w-4" />
                  New Character
                </Button>
                <ScrollArea className="max-h-[480px]">
                  <div className="space-y-1">
                    {characters.map((c) => (
                      <div
                        key={c.id}
                        className={cn(
                          "rounded-md px-3 py-2 text-sm cursor-pointer transition-colors",
                          selectedCharId === c.id
                            ? "bg-primary/12 font-medium text-foreground"
                            : "hover:bg-accent text-muted-foreground",
                        )}
                        onClick={() => selectCharacter(c)}
                      >
                        <div className="truncate">{c.name}</div>
                        <div className="text-[11px] text-muted-foreground">v{c.version}</div>
                      </div>
                    ))}
                  </div>
                </ScrollArea>
              </Card>

              <Card>
                <CardHeader className="flex flex-row items-center justify-between">
                  <div>
                    <CardTitle>
                      {selectedCharId === "__new__" ? "New Character" : `Edit: ${charName}`}
                    </CardTitle>
                    <CardDescription>
                      Snapshots are copied when a room starts; edits update new rooms cleanly.
                    </CardDescription>
                  </div>
                  {selectedCharId !== "__new__" && (
                    <Button size="sm" variant="destructive" onClick={() => void deleteCharacter()}>
                      <Trash2 className="h-4 w-4" />
                      Delete
                    </Button>
                  )}
                </CardHeader>
                <CardContent className="space-y-4">
                  <div className="space-y-1.5">
                    <Label htmlFor="char-name">Name</Label>
                    <Input
                      id="char-name"
                      value={charName}
                      onChange={(e) => setCharName(e.target.value)}
                      placeholder="Hana"
                    />
                  </div>
                  <div className="space-y-1.5">
                    <Label htmlFor="char-personality">Personality</Label>
                    <Textarea
                      id="char-personality"
                      value={personality}
                      onChange={(e) => setPersonality(e.target.value)}
                      placeholder="Warm, sharp-witted archivist who speaks concisely."
                    />
                  </div>
                  <div className="grid gap-4 md:grid-cols-2">
                    <div className="space-y-1.5">
                      <Label htmlFor="char-bg">Background</Label>
                      <Textarea
                        id="char-bg"
                        value={background}
                        onChange={(e) => setBackground(e.target.value)}
                      />
                    </div>
                    <div className="space-y-1.5">
                      <Label htmlFor="char-app">Appearance</Label>
                      <Textarea
                        id="char-app"
                        value={appearance}
                        onChange={(e) => setAppearance(e.target.value)}
                      />
                    </div>
                  </div>
                  <div className="space-y-1.5">
                    <Label htmlFor="char-values">Initial Values (one per line)</Label>
                    <Textarea
                      id="char-values"
                      value={initialValuesText}
                      onChange={(e) => setInitialValuesText(e.target.value)}
                      placeholder="Keep promises carefully.&#10;Verify coordinates before acting."
                    />
                  </div>
                  <div className="grid gap-4 md:grid-cols-3">
                    <div className="space-y-1.5">
                      <Label htmlFor="char-chat-model">Chat Model Override</Label>
                      <Input
                        id="char-chat-model"
                        list="discovered-models"
                        value={chatModel}
                        onChange={(e) => setChatModel(e.target.value)}
                        placeholder="Default global role"
                      />
                    </div>
                    <div className="space-y-1.5">
                      <Label htmlFor="char-mem-model">Memory Model Override</Label>
                      <Input
                        id="char-mem-model"
                        list="discovered-models"
                        value={memoryModel}
                        onChange={(e) => setMemoryModel(e.target.value)}
                        placeholder="Default global role"
                      />
                    </div>
                    <div className="space-y-1.5">
                      <Label htmlFor="char-approval">Approval Policy</Label>
                      <select
                        id="char-approval"
                        className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
                        value={approvalOverride}
                        onChange={(e) => setApprovalOverride(e.target.value as "" | ApprovalMode)}
                      >
                        <option value="">Global default</option>
                        <option value="bypass">bypass</option>
                        <option value="auto-detection">auto-detection</option>
                        <option value="request">request</option>
                      </select>
                    </div>
                  </div>
                  <datalist id="discovered-models">
                    {models.map((m) => (
                      <option key={m.id} value={m.id} />
                    ))}
                  </datalist>
                  <div className="flex justify-end">
                    <Button onClick={() => void saveCharacter()}>
                      <Save className="h-4 w-4" />
                      {selectedCharId === "__new__" ? "Create Character" : "Save Changes"}
                    </Button>
                  </div>
                </CardContent>
              </Card>
            </div>
          </TabsContent>

          <TabsContent value="personas">
            <div className="grid grid-cols-1 gap-5 md:grid-cols-[240px_1fr]">
              <Card className="p-2 h-fit">
                <Button
                  size="sm"
                  className="mb-2 w-full"
                  variant="outline"
                  onClick={() => selectPersona(null)}
                >
                  <Plus className="h-4 w-4" />
                  New Persona
                </Button>
                <div className="space-y-1">
                  {personas.map((p) => (
                    <div
                      key={p.id}
                      className={cn(
                        "rounded-md px-3 py-2 text-sm cursor-pointer transition-colors",
                        selectedPersonaId === p.id
                          ? "bg-primary/12 font-medium text-foreground"
                          : "hover:bg-accent text-muted-foreground",
                      )}
                      onClick={() => selectPersona(p)}
                    >
                      <div className="flex items-center justify-between gap-2">
                        <span className="truncate">{p.name}</span>
                        {p.is_default && <Badge variant="secondary">default</Badge>}
                      </div>
                    </div>
                  ))}
                </div>
              </Card>

              <Card>
                <CardHeader className="flex flex-row items-center justify-between">
                  <div>
                    <CardTitle>
                      {selectedPersonaId === "__new__" ? "New User Persona" : `Edit: ${personaName}`}
                    </CardTitle>
                    <CardDescription>
                      Represents your identity in conversations with characters.
                    </CardDescription>
                  </div>
                  {selectedPersonaId !== "__new__" && (
                    <Button size="sm" variant="destructive" onClick={() => void deletePersona()}>
                      <Trash2 className="h-4 w-4" />
                      Delete
                    </Button>
                  )}
                </CardHeader>
                <CardContent className="space-y-4">
                  <div className="space-y-1.5">
                    <Label htmlFor="p-name">Name</Label>
                    <Input
                      id="p-name"
                      value={personaName}
                      onChange={(e) => setPersonaName(e.target.value)}
                      placeholder="Alex"
                    />
                  </div>
                  <div className="space-y-1.5">
                    <Label htmlFor="p-desc">Description</Label>
                    <Textarea
                      id="p-desc"
                      value={personaDesc}
                      onChange={(e) => setPersonaDesc(e.target.value)}
                      placeholder="A late-night visitor carrying a notebook."
                    />
                  </div>
                  <div className="flex items-center gap-3">
                    <Switch
                      id="p-default"
                      checked={personaDefault}
                      onCheckedChange={setPersonaDefault}
                    />
                    <Label htmlFor="p-default">Default Persona</Label>
                  </div>
                  <div className="flex justify-end">
                    <Button onClick={() => void savePersona()}>
                      <Save className="h-4 w-4" />
                      {selectedPersonaId === "__new__" ? "Create Persona" : "Save Persona"}
                    </Button>
                  </div>
                </CardContent>
              </Card>
            </div>
          </TabsContent>
        </Tabs>
      </div>
    </div>
  );
}
