import { useCallback, useEffect, useRef, useState, type KeyboardEvent } from "react";
import {
  AlertTriangle,
  Bot,
  Brain,
  MessageSquarePlus,
  PanelRight,
  RotateCcw,
  Send,
  SlidersHorizontal,
  Square,
  Trash2,
  User,
  Zap,
} from "lucide-react";
import type { QClient } from "@/client/api";
import { QAPIError } from "@/client/api";
import { subscribeRoomStream } from "@/client/sse";
import type {
  ApprovalMode,
  Character,
  DecisionModelInfo,
  LLMRolesConfig,
  MessageBubble,
  ModelInfo,
  Persona,
  RoomDetail,
  RoomMemoriesResponse,
  RoomMode,
  RoomSummary,
  ToolInvocation,
  UserQuestion,
} from "@/client/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ModelSelect } from "@/components/ui/model-select";
import { ReasoningSelect, reconcileReasoningEffort } from "@/components/ui/reasoning-select";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Separator } from "@/components/ui/separator";
import { Textarea } from "@/components/ui/textarea";
import {
  ApprovalRequestCard,
  LoomArtifactDrawer,
  ToolInvocationCard,
  UserQuestionCard,
} from "@/features/tools/ToolCards";
import { cn } from "@/lib/utils";

interface StreamingBubble {
  messageId: string;
  actorId: string;
  actorName: string;
  content: string;
}

export function RoomsView({ client }: { client: QClient }) {
  const [rooms, setRooms] = useState<RoomSummary[]>([]);
  const [selectedRoomId, setSelectedRoomId] = useState<string>("");
  const [detail, setDetail] = useState<RoomDetail | null>(null);
  const [messages, setMessages] = useState<MessageBubble[]>([]);
  const [nextBeforeSeq, setNextBeforeSeq] = useState<number | undefined>();
  const [memories, setMemories] = useState<RoomMemoriesResponse | null>(null);
  const [toolInvocations, setToolInvocations] = useState<ToolInvocation[]>([]);
  const [pendingApprovals, setPendingApprovals] = useState<ToolInvocation[]>([]);
  const [pendingQuestions, setPendingQuestions] = useState<UserQuestion[]>([]);
  const [openArtifactId, setOpenArtifactId] = useState<string | null>(null);
  const [streaming, setStreaming] = useState<StreamingBubble | null>(null);
  const [actorPhase, setActorPhase] = useState<string>("");
  const [composerText, setComposerText] = useState<string>("");
  const [showInspector, setShowInspector] = useState<boolean>(true);
  const [errorMsg, setErrorMsg] = useState<string>("");
  const [busySubmit, setBusySubmit] = useState<boolean>(false);

  const [createOpen, setCreateOpen] = useState<boolean>(false);
  const [personas, setPersonas] = useState<Persona[]>([]);
  const [characters, setCharacters] = useState<Character[]>([]);
  const [newTitle, setNewTitle] = useState<string>("");
  const [newMode, setNewMode] = useState<RoomMode>("direct");
  const [newPersonaId, setNewPersonaId] = useState<string>("");
  const [newCharacterId, setNewCharacterId] = useState<string>("");
  const [newCharacterIds, setNewCharacterIds] = useState<string[]>([]);
  const [newMaxTurns, setNewMaxTurns] = useState<number>(3);
  const [newWorkDir, setNewWorkDir] = useState<string>("");
  const [inspectedMemberId, setInspectedMemberId] = useState<string>("");

  const [editOpen, setEditOpen] = useState<boolean>(false);
  const [editTitle, setEditTitle] = useState<string>("");
  const [editWorkDir, setEditWorkDir] = useState<string>("");
  const [editApprovalOverride, setEditApprovalOverride] = useState<"" | ApprovalMode>("");
  const [editCharacterIds, setEditCharacterIds] = useState<string[]>([]);
  const [editMaxTurns, setEditMaxTurns] = useState<number>(3);
  const [models, setModels] = useState<ModelInfo[]>([]);
  const [defaultRoles, setDefaultRoles] = useState<LLMRolesConfig>({});
  const [decisionModels, setDecisionModels] = useState<DecisionModelInfo[]>([]);
  const [editChatModel, setEditChatModel] = useState<string>("");
  const [editChatReasoning, setEditChatReasoning] = useState<string>("");
  const [editMemoryModel, setEditMemoryModel] = useState<string>("");
  const [editMemoryReasoning, setEditMemoryReasoning] = useState<string>("");
  const [editDecisionModel, setEditDecisionModel] = useState<string>("");

  const timelineEndRef = useRef<HTMLDivElement | null>(null);

  const loadRooms = useCallback(async () => {
    try {
      const res = await client.listRooms();
      setRooms(res.items);
      setSelectedRoomId((prev) => {
        if (prev && res.items.some((r) => r.room.id === prev)) return prev;
        return res.items[0]?.room.id ?? "";
      });
    } catch (err) {
      setErrorMsg(err instanceof Error ? err.message : "Failed to load rooms");
    }
  }, [client]);

  const loadRoomState = useCallback(
    async (roomId: string) => {
      if (!roomId) {
        setDetail(null);
        setMessages([]);
        setMemories(null);
        setPendingApprovals([]);
        setPendingQuestions([]);
        return;
      }
      try {
        const [d, m, mem] = await Promise.all([
          client.getRoom(roomId),
          client.listMessages(roomId, { limit: 100 }),
          client.getRoomMemories(roomId),
        ]);
        setDetail(d);
        setMessages(m.items);
        setNextBeforeSeq(m.next_before_seq);
        setMemories(mem);
        setPendingApprovals(d.pending_approvals ?? []);
        setPendingQuestions(d.pending_questions ?? []);
      } catch (err) {
        setErrorMsg(err instanceof Error ? err.message : "Failed to load room");
      }
    },
    [client],
  );

  useEffect(() => {
    void loadRooms();
  }, [loadRooms]);

  useEffect(() => {
    setStreaming(null);
    setActorPhase("");
    setToolInvocations([]);
    setInspectedMemberId("");
    void loadRoomState(selectedRoomId);
  }, [selectedRoomId, loadRoomState]);

  useEffect(() => {
    if (!selectedRoomId) return;
    const unsubscribe = subscribeRoomStream(selectedRoomId, {
      onReconnect: () => {
        void loadRoomState(selectedRoomId);
      },
      onEvent: (ev) => {
        const payload = (ev.data ?? {}) as Record<string, unknown>;
        switch (ev.event) {
          case "turn.started": {
            setActorPhase("starting");
            setDetail((prev) => (prev ? { ...prev, active_turn: true } : prev));
            void client.listMessages(selectedRoomId, { limit: 100 }).then((res) => {
              setMessages(res.items);
              setNextBeforeSeq(res.next_before_seq);
            });
            break;
          }
          case "actor.status": {
            const phase = typeof payload.phase === "string" ? payload.phase : "";
            setActorPhase(phase);
            break;
          }
          case "bubble.delta": {
            const messageId = String(payload.message_id ?? "");
            const actorId = String(payload.actor_id ?? "");
            const delta = String(payload.delta ?? "");
            if (!messageId || !delta) break;
            setStreaming((prev) => {
              if (prev && prev.messageId === messageId) {
                return { ...prev, content: prev.content + delta };
              }
              return {
                messageId,
                actorId,
                actorName:
                  detail?.members.find((m) => m.character_id === actorId)?.character_snapshot.name ??
                  "Character",
                content: delta,
              };
            });
            break;
          }
          case "bubble.completed": {
            const msg = payload.message as MessageBubble | undefined;
            if (!msg?.id) break;
            setStreaming((prev) => (prev?.messageId === msg.id ? null : prev));
            setMessages((prev) =>
              prev.some((existing) => existing.id === msg.id) ? prev : [...prev, msg],
            );
            break;
          }
          case "tool.started": {
            const invId = String(payload.invocation_id ?? "");
            if (!invId) break;
            setToolInvocations((prev) => {
              if (prev.some((i) => i.id === invId)) return prev;
              return [
                ...prev,
                {
                  id: invId,
                  turn_id: String(payload.turn_id ?? ""),
                  room_id: selectedRoomId,
                  actor_id: String(payload.actor_id ?? ""),
                  call_id: "",
                  tool_name: String(payload.tool_name ?? "tool"),
                  tool_source: "builtin",
                  args_json: "{}",
                  gate_mode: (payload.gate_mode as ToolInvocation["gate_mode"]) ?? "auto-detection",
                  status: "running",
                  created_at: Date.now(),
                  updated_at: Date.now(),
                },
              ];
            });
            break;
          }
          case "tool.progress": {
            const invId = String(payload.invocation_id ?? "");
            const msg = String(payload.message ?? "");
            if (!invId || !msg) break;
            setToolInvocations((prev) =>
              prev.map((i) => (i.id === invId ? { ...i, result_summary: msg } : i)),
            );
            break;
          }
          case "tool.completed": {
            const inv = payload.invocation as ToolInvocation | undefined;
            if (!inv?.id) break;
            setToolInvocations((prev) => {
              const exists = prev.some((i) => i.id === inv.id);
              return exists ? prev.map((i) => (i.id === inv.id ? inv : i)) : [...prev, inv];
            });
            break;
          }
          case "approval.required": {
            const inv = payload.invocation as ToolInvocation | undefined;
            if (!inv?.id) break;
            setActorPhase("awaiting_approval");
            setDetail((prev) => (prev ? { ...prev, active_turn: false } : prev));
            setPendingApprovals((prev) =>
              prev.some((i) => i.id === inv.id) ? prev : [...prev, inv],
            );
            break;
          }
          case "approval.resolved": {
            const invId = String(payload.invocation_id ?? "");
            setPendingApprovals((prev) => prev.filter((i) => i.id !== invId));
            setActorPhase("resuming");
            break;
          }
          case "question.required": {
            const q = payload.question as UserQuestion | undefined;
            if (!q?.id) break;
            setActorPhase("awaiting_user");
            setDetail((prev) => (prev ? { ...prev, active_turn: false } : prev));
            setPendingQuestions((prev) =>
              prev.some((existing) => existing.id === q.id) ? prev : [...prev, q],
            );
            break;
          }
          case "question.resolved": {
            const qId = String(payload.question_id ?? "");
            setPendingQuestions((prev) => prev.filter((q) => q.id !== qId));
            setActorPhase("resuming");
            break;
          }
          case "turn.intercepted": {
            setStreaming(null);
            break;
          }
          case "turn.completed":
          case "turn.failed": {
            setStreaming(null);
            setActorPhase("");
            void loadRoomState(selectedRoomId);
            break;
          }
        }
      },
    });
    return unsubscribe;
  }, [selectedRoomId, client, loadRoomState, detail?.members]);

  useEffect(() => {
    timelineEndRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [messages.length, streaming?.content, pendingApprovals.length, pendingQuestions.length]);

  const openCreateDialog = async () => {
    setErrorMsg("");
    try {
      const [pRes, cRes] = await Promise.all([client.listPersonas(), client.listCharacters()]);
      setPersonas(pRes.items);
      setCharacters(cRes.items);
      const defPersona = pRes.items.find((p) => p.is_default) ?? pRes.items[0];
      setNewMode("direct");
      setNewPersonaId(defPersona?.id ?? "");
      setNewCharacterId(cRes.items[0]?.id ?? "");
      setNewCharacterIds(cRes.items.slice(0, 2).map((c) => c.id));
      setNewMaxTurns(3);
      setNewWorkDir("");
      setNewTitle(cRes.items[0] ? `${cRes.items[0].name}` : "New Chat");
      setCreateOpen(true);
    } catch (err) {
      setErrorMsg(err instanceof Error ? err.message : "Failed to load personas/characters");
    }
  };

  const toggleGroupCharacter = (charId: string) => {
    setNewCharacterIds((prev) =>
      prev.includes(charId) ? prev.filter((id) => id !== charId) : [...prev, charId],
    );
  };

  const handleCreateRoom = async () => {
    const selectedIds = newMode === "group" ? newCharacterIds : newCharacterId ? [newCharacterId] : [];
    if (!newTitle.trim() || !newPersonaId || selectedIds.length === 0) {
      setErrorMsg("Room title, persona, and at least one character are required.");
      return;
    }
    try {
      const created = await client.createRoom({
        title: newTitle.trim(),
        mode: newMode,
        persona_id: newPersonaId,
        character_ids: selectedIds,
        max_turns_per_input: newMode === "group" ? Math.max(1, Math.min(20, newMaxTurns || 3)) : undefined,
        work_dir: newWorkDir.trim() || undefined,
      });
      setCreateOpen(false);
      await loadRooms();
      setSelectedRoomId(created.room.id);
    } catch (err) {
      setErrorMsg(err instanceof Error ? err.message : "Failed to create room");
    }
  };

  const openEditDialog = async () => {
    if (!detail) return;
    setErrorMsg("");
    try {
      const [cRes, mRes, dRes] = await Promise.all([
        client.listCharacters(),
        client.listModels(),
        client.listDecisionModels(),
      ]);
      setCharacters(cRes.items);
      setModels(mRes.models);
      setDefaultRoles(mRes.default_roles ?? {});
      setDecisionModels(dRes.models);
      setEditTitle(detail.room.title);
      setEditWorkDir(detail.room.work_dir ?? "");
      setEditApprovalOverride(detail.room.approval_mode_override ?? "");
      setEditCharacterIds(detail.members.filter((m) => m.active).map((m) => m.character_id));
      setEditMaxTurns(detail.room.max_turns_per_input || 3);
      setEditChatModel(detail.room.model_overrides?.chat_model ?? "");
      setEditChatReasoning(detail.room.model_overrides?.reasoning_effort ?? "");
      setEditMemoryModel(detail.room.model_overrides?.memory_model ?? "");
      setEditMemoryReasoning(detail.room.model_overrides?.memory_reasoning_effort ?? "");
      setEditDecisionModel(detail.room.model_overrides?.decision_model ?? "");
      setEditOpen(true);
    } catch (err) {
      setErrorMsg(err instanceof Error ? err.message : "Failed to load room settings");
    }
  };

  const toggleEditCharacter = (charId: string) => {
    setEditCharacterIds((prev) =>
      prev.includes(charId) ? prev.filter((id) => id !== charId) : [...prev, charId],
    );
  };

  const handleSaveRoomSettings = async () => {
    if (!detail || !editTitle.trim()) return;
    if (detail.room.mode === "group" && editCharacterIds.length === 0) {
      setErrorMsg("Group rooms require at least one active character.");
      return;
    }
    const hasModelOverrides =
      editChatModel ||
      editChatReasoning ||
      editMemoryModel ||
      editMemoryReasoning ||
      editDecisionModel;
    const modelOverrides = hasModelOverrides
      ? {
          ...(editChatModel ? { chat_model: editChatModel } : {}),
          ...(editChatReasoning ? { reasoning_effort: editChatReasoning } : {}),
          ...(editMemoryModel ? { memory_model: editMemoryModel } : {}),
          ...(editMemoryReasoning ? { memory_reasoning_effort: editMemoryReasoning } : {}),
          ...(editDecisionModel ? { decision_model: editDecisionModel } : {}),
        }
      : {};
    try {
      await client.patchRoom(detail.room.id, {
        expected_version: detail.room.version,
        title: editTitle.trim(),
        work_dir: editWorkDir.trim(),
        ...(editApprovalOverride ? { approval_mode_override: editApprovalOverride } : {}),
        model_overrides: modelOverrides,
        ...(detail.room.mode === "group"
          ? {
              character_ids: editCharacterIds,
              max_turns_per_input: Math.max(1, Math.min(20, editMaxTurns || 3)),
            }
          : {}),
      });
      setEditOpen(false);
      await Promise.all([loadRooms(), loadRoomState(detail.room.id)]);
    } catch (err) {
      setErrorMsg(err instanceof Error ? err.message : "Failed to update room");
    }
  };

  const handleDeleteRoom = async (roomId: string) => {
    try {
      await client.deleteRoom(roomId);
      if (selectedRoomId === roomId) {
        setSelectedRoomId("");
      }
      await loadRooms();
    } catch (err) {
      setErrorMsg(err instanceof Error ? err.message : "Failed to delete room");
    }
  };

  const handleLoadOlder = async () => {
    if (!selectedRoomId || !nextBeforeSeq) return;
    try {
      const older = await client.listMessages(selectedRoomId, {
        before_seq: nextBeforeSeq,
        limit: 50,
      });
      setMessages((prev) => {
        const seen = new Set(prev.map((m) => m.id));
        return [...older.items.filter((m) => !seen.has(m.id)), ...prev];
      });
      setNextBeforeSeq(older.next_before_seq);
    } catch (err) {
      setErrorMsg(err instanceof Error ? err.message : "Failed to load older messages");
    }
  };

  const handleSendOrCutIn = async () => {
    const trimmed = composerText.trim();
    if (!selectedRoomId || !trimmed || busySubmit) return;
    setErrorMsg("");
    setBusySubmit(true);
    try {
      if (detail?.active_turn) {
        await client.interceptTurn(selectedRoomId, trimmed);
      } else {
        await client.submitTurn(selectedRoomId, {
          request_id: crypto.randomUUID(),
          text: trimmed,
        });
      }
      setComposerText("");
    } catch (err) {
      if (err instanceof QAPIError) {
        setErrorMsg(`${err.code}: ${err.message}`);
      } else {
        setErrorMsg(err instanceof Error ? err.message : "Failed to submit");
      }
    } finally {
      setBusySubmit(false);
    }
  };

  const handleStop = async () => {
    if (!selectedRoomId) return;
    try {
      await client.interceptTurn(selectedRoomId, "");
    } catch (err) {
      setErrorMsg(err instanceof Error ? err.message : "Failed to stop turn");
    }
  };

  const handleRetry = async (turnId: string) => {
    if (!selectedRoomId) return;
    setErrorMsg("");
    try {
      await client.retryTurn(selectedRoomId, turnId);
      setDetail((prev) => (prev ? { ...prev, active_turn: true } : prev));
    } catch (err) {
      setErrorMsg(err instanceof Error ? err.message : "Failed to retry turn");
    }
  };

  const handleResolveApproval = async (
    invocationId: string,
    decision: "approve" | "reject",
    reason: string,
  ) => {
    if (!selectedRoomId) return;
    setErrorMsg("");
    try {
      await client.resolveApproval(selectedRoomId, invocationId, decision, reason);
      setPendingApprovals((prev) => prev.filter((i) => i.id !== invocationId));
      setDetail((prev) => (prev ? { ...prev, active_turn: true } : prev));
    } catch (err) {
      setErrorMsg(err instanceof Error ? err.message : "Failed to resolve approval");
    }
  };

  const handleAnswerQuestion = async (questionId: string, answerText: string) => {
    if (!selectedRoomId) return;
    setErrorMsg("");
    try {
      await client.resolveQuestion(selectedRoomId, questionId, answerText);
      setPendingQuestions((prev) => prev.filter((q) => q.id !== questionId));
      setDetail((prev) => (prev ? { ...prev, active_turn: true } : prev));
    } catch (err) {
      setErrorMsg(err instanceof Error ? err.message : "Failed to submit answer");
    }
  };

  const onComposerKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      void handleSendOrCutIn();
    }
  };

  const activeTurn = detail?.active_turn ?? false;
  const latestTurn = detail?.turns[0];
  const failedTurn =
    !activeTurn && latestTurn && (latestTurn.status === "failed" || latestTurn.status === "cancelled")
      ? latestTurn
      : null;
  const activeMembers = detail?.members.filter((m) => m.active) ?? [];
  const primaryMember =
    detail?.members.find((m) => m.character_id === inspectedMemberId) ??
    activeMembers[0] ??
    detail?.members[0];

  return (
    <div className="flex h-full min-h-0 flex-1 overflow-hidden">
      {/* Room Sidebar */}
      <aside className="flex w-68 shrink-0 flex-col border-r border-border bg-card/50">
        <div className="flex items-center justify-between px-4 py-3 border-b border-border">
          <span className="text-sm font-semibold tracking-tight">Rooms</span>
          <Button size="sm" onClick={() => void openCreateDialog()}>
            <MessageSquarePlus className="h-4 w-4" />
            New
          </Button>
        </div>
        <ScrollArea className="flex-1 p-2">
          {rooms.length === 0 ? (
            <div className="px-3 py-8 text-center text-xs text-muted-foreground">
              No rooms yet. Create a character &amp; persona first, then start a room.
            </div>
          ) : (
            <div className="space-y-1">
              {rooms.map((item) => {
                const selected = item.room.id === selectedRoomId;
                const activeNames = item.members
                  .filter((m) => m.active)
                  .map((m) => m.character_snapshot.name);
                const charName =
                  activeNames.length > 0
                    ? activeNames.join(", ")
                    : (item.members[0]?.character_snapshot.name ?? "No character");
                return (
                  <div
                    key={item.room.id}
                    className={cn(
                      "group flex items-center justify-between rounded-lg px-3 py-2.5 text-sm transition-colors cursor-pointer",
                      selected
                        ? "bg-primary/12 text-foreground font-medium"
                        : "hover:bg-accent/70 text-muted-foreground hover:text-foreground",
                    )}
                    onClick={() => setSelectedRoomId(item.room.id)}
                  >
                    <div className="min-w-0 flex-1">
                      <div className="truncate">{item.room.title}</div>
                      <div className="truncate text-xs text-muted-foreground">
                        {charName} · {item.room.persona_snapshot.name}
                      </div>
                    </div>
                    <button
                      type="button"
                      title="Delete room"
                      className="opacity-0 group-hover:opacity-100 rounded p-1 text-muted-foreground hover:text-destructive cursor-pointer"
                      onClick={(e) => {
                        e.stopPropagation();
                        void handleDeleteRoom(item.room.id);
                      }}
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </button>
                  </div>
                );
              })}
            </div>
          )}
        </ScrollArea>
      </aside>

      {/* Main Conversation */}
      <main className="flex min-w-0 flex-1 flex-col bg-background">
        {!detail ? (
          <div className="flex flex-1 flex-col items-center justify-center gap-3 p-8 text-center">
            <Bot className="h-10 w-10 text-muted-foreground/60" />
            <div className="text-base font-medium">Select or create a chat room</div>
            <p className="max-w-md text-sm text-muted-foreground">
              Characters converse through chat bubbles, invoke tools behind an approval gate,
              remember observed experiences, and evolve their affect and values across turns.
            </p>
            <Button onClick={() => void openCreateDialog()}>Create Room</Button>
          </div>
        ) : (
          <>
            <header className="flex items-center justify-between border-b border-border px-5 py-3 bg-card/40">
              <div className="min-w-0">
                <div className="flex items-center gap-2.5 flex-wrap">
                  <h2 className="truncate text-base font-semibold">{detail.room.title}</h2>
                  <Badge variant="secondary">
                    {activeMembers.length > 0
                      ? activeMembers.map((m) => m.character_snapshot.name).join(", ")
                      : (primaryMember?.character_snapshot.name ?? "—")}
                  </Badge>
                  {detail.room.work_dir && (
                    <Badge variant="outline" className="font-mono text-[11px] max-w-xs truncate" title={detail.room.work_dir}>
                      {detail.room.work_dir}
                    </Badge>
                  )}
                  {(activeTurn || actorPhase) && (
                    <Badge variant="default" className="animate-pulse">
                      {actorPhase || "thinking"}
                    </Badge>
                  )}
                </div>
                <p className="text-xs text-muted-foreground">
                  Persona: {detail.room.persona_snapshot.name} · Mode: {detail.room.mode}
                </p>
              </div>
              <div className="flex items-center gap-2 shrink-0">
                <Button
                  variant="outline"
                  size="sm"
                  disabled={activeTurn}
                  onClick={() => void openEditDialog()}
                  title={activeTurn ? "Finish or stop the active turn before editing room settings" : "Edit room settings"}
                >
                  <SlidersHorizontal className="h-4 w-4" />
                  Room Settings
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => setShowInspector((prev) => !prev)}
                >
                  <PanelRight className="h-4 w-4" />
                  {showInspector ? "Hide State" : "Show State"}
                </Button>
              </div>
            </header>

            {errorMsg && (
              <div className="mx-5 mt-3 flex items-center justify-between rounded-lg border border-destructive/40 bg-destructive/10 px-3.5 py-2 text-xs text-destructive">
                <span>{errorMsg}</span>
                <button
                  type="button"
                  className="underline cursor-pointer"
                  onClick={() => setErrorMsg("")}
                >
                  Dismiss
                </button>
              </div>
            )}

            <ScrollArea className="flex-1 px-5 py-4">
              <div className="mx-auto flex max-w-3xl flex-col gap-3">
                {nextBeforeSeq && (
                  <div className="flex justify-center pb-2">
                    <Button variant="outline" size="sm" onClick={() => void handleLoadOlder()}>
                      Load earlier messages
                    </Button>
                  </div>
                )}

                {messages.map((msg) => {
                  const isUser = msg.role === "user";
                  return (
                    <div
                      key={msg.id}
                      className={cn("flex w-full gap-2.5", isUser ? "justify-end" : "justify-start")}
                    >
                      {!isUser && (
                        <div className="mt-1 flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-secondary text-secondary-foreground">
                          <Bot className="h-4 w-4" />
                        </div>
                      )}
                      <div
                        className={cn(
                          "max-w-[78%] rounded-2xl px-4 py-2.5 text-sm shadow-xs",
                          isUser
                            ? "bg-primary text-primary-foreground rounded-br-sm"
                            : "border border-border bg-card text-card-foreground rounded-bl-sm",
                        )}
                      >
                        <div
                          className={cn(
                            "mb-1 flex items-center gap-2 text-[11px]",
                            isUser ? "text-primary-foreground/80" : "text-muted-foreground",
                          )}
                        >
                          <span className="font-medium">{msg.author_name}</span>
                          <span>
                            {new Date(msg.created_at).toLocaleTimeString([], {
                              hour: "2-digit",
                              minute: "2-digit",
                            })}
                          </span>
                        </div>
                        <div className="whitespace-pre-wrap leading-relaxed">{msg.content}</div>
                      </div>
                      {isUser && (
                        <div className="mt-1 flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-primary/15 text-primary">
                          <User className="h-4 w-4" />
                        </div>
                      )}
                    </div>
                  );
                })}

                {toolInvocations.map((inv) => (
                  <ToolInvocationCard
                    key={inv.id}
                    invocation={inv}
                    onOpenArtifact={(id) => setOpenArtifactId(id)}
                  />
                ))}

                {pendingApprovals.map((inv) => (
                  <ApprovalRequestCard
                    key={inv.id}
                    invocation={inv}
                    onResolve={handleResolveApproval}
                  />
                ))}

                {pendingQuestions.map((q) => (
                  <UserQuestionCard key={q.id} question={q} onAnswer={handleAnswerQuestion} />
                ))}

                {streaming && (
                  <div className="flex w-full justify-start gap-2.5">
                    <div className="mt-1 flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-secondary text-secondary-foreground">
                      <Bot className="h-4 w-4" />
                    </div>
                    <div className="max-w-[78%] rounded-2xl rounded-bl-sm border border-primary/40 bg-card px-4 py-2.5 text-sm shadow-xs">
                      <div className="mb-1 flex items-center gap-2 text-[11px] text-muted-foreground">
                        <span className="font-medium">{streaming.actorName}</span>
                        <span>streaming…</span>
                      </div>
                      <div className="whitespace-pre-wrap leading-relaxed">{streaming.content}</div>
                    </div>
                  </div>
                )}

                {failedTurn && (
                  <div className="flex items-center justify-between rounded-lg border border-destructive/40 bg-destructive/8 px-4 py-2.5 text-xs">
                    <div className="flex items-center gap-2">
                      <AlertTriangle className="h-4 w-4 text-destructive" />
                      <span>
                        Turn {failedTurn.status}: {failedTurn.error_message || failedTurn.error_code}
                      </span>
                    </div>
                    <Button size="sm" variant="outline" onClick={() => void handleRetry(failedTurn.id)}>
                      <RotateCcw className="h-3.5 w-3.5" />
                      Retry
                    </Button>
                  </div>
                )}
                <div ref={timelineEndRef} />
              </div>
            </ScrollArea>

            {/* Composer */}
            <div className="border-t border-border bg-card/40 px-5 py-3">
              <div className="mx-auto flex max-w-3xl items-end gap-2">
                <Textarea
                  value={composerText}
                  onChange={(e) => setComposerText(e.target.value)}
                  onKeyDown={onComposerKeyDown}
                  placeholder={
                    activeTurn
                      ? "Type to cut in mid-turn, or press Stop to cancel…"
                      : "Write a message to chat… (Enter to send, Shift+Enter for newline)"
                  }
                  className="min-h-[52px] max-h-40 resize-y bg-background"
                />
                {activeTurn ? (
                  <div className="flex flex-col gap-1.5">
                    <Button
                      size="sm"
                      disabled={!composerText.trim() || busySubmit}
                      onClick={() => void handleSendOrCutIn()}
                    >
                      <Zap className="h-3.5 w-3.5" />
                      Cut-in
                    </Button>
                    <Button size="sm" variant="destructive" onClick={() => void handleStop()}>
                      <Square className="h-3.5 w-3.5" />
                      Stop
                    </Button>
                  </div>
                ) : (
                  <Button
                    disabled={!composerText.trim() || busySubmit}
                    onClick={() => void handleSendOrCutIn()}
                  >
                    <Send className="h-4 w-4" />
                    Send
                  </Button>
                )}
              </div>
            </div>
          </>
        )}
      </main>

      {/* Right State & Memories Inspector */}
      {detail && showInspector && primaryMember && (
        <aside className="w-80 shrink-0 border-l border-border bg-card/40 flex flex-col">
          <div className="flex items-center gap-2 border-b border-border px-4 py-3">
            <Brain className="h-4 w-4 text-primary" />
            <span className="text-sm font-semibold">Actor State &amp; Memory</span>
          </div>
          {detail.members.length > 1 && (
            <div className="flex flex-wrap gap-1 border-b border-border px-3 py-2 bg-muted/30">
              {detail.members.map((m) => {
                const selected = m.character_id === primaryMember.character_id;
                return (
                  <button
                    key={m.character_id}
                    type="button"
                    onClick={() => setInspectedMemberId(m.character_id)}
                    className={cn(
                      "rounded-md px-2 py-1 text-xs font-medium transition-colors cursor-pointer",
                      selected
                        ? "bg-primary text-primary-foreground"
                        : "bg-background text-muted-foreground hover:text-foreground border border-border",
                    )}
                  >
                    {m.character_snapshot.name}
                    {!m.active && " (off)"}
                  </button>
                );
              })}
            </div>
          )}
          <ScrollArea className="flex-1 p-4">
            <div className="space-y-5 text-xs">
              <section className="space-y-2">
                <div className="font-semibold text-foreground">Affect &amp; Relationship</div>
                <Card className="p-3 space-y-1.5 bg-background">
                  <div className="flex justify-between">
                    <span className="text-muted-foreground">Self Valence</span>
                    <span className="font-mono">{primaryMember.actor_state.self.valence}</span>
                  </div>
                  <div className="flex justify-between">
                    <span className="text-muted-foreground">Self Activation</span>
                    <span className="font-mono">{primaryMember.actor_state.self.activation}</span>
                  </div>
                  <div className="flex justify-between">
                    <span className="text-muted-foreground">Stressed</span>
                    <span>{primaryMember.actor_state.self.stressed ? "Yes" : "No"}</span>
                  </div>
                  {primaryMember.actor_state.relationships?.[detail.room.persona_id] && (
                    <>
                      <Separator className="my-1.5" />
                      <div className="flex justify-between">
                        <span className="text-muted-foreground">User Trust</span>
                        <span className="font-mono">
                          {primaryMember.actor_state.relationships[detail.room.persona_id].trust}
                        </span>
                      </div>
                      <div className="flex justify-between">
                        <span className="text-muted-foreground">User Valence</span>
                        <span className="font-mono">
                          {primaryMember.actor_state.relationships[detail.room.persona_id].valence}
                        </span>
                      </div>
                    </>
                  )}
                </Card>
              </section>

              <section className="space-y-2">
                <div className="flex items-center justify-between font-semibold">
                  <span>Values</span>
                  <Badge variant="outline">rev {primaryMember.actor_state.values_revision}</Badge>
                </div>
                {(primaryMember.actor_state.values ?? []).length === 0 ? (
                  <p className="text-muted-foreground">No consolidated values recorded yet.</p>
                ) : (
                  <ul className="space-y-1.5">
                    {(primaryMember.actor_state.values ?? []).map((v) => (
                      <li key={v.id} className="rounded-md border border-border bg-background p-2">
                        {v.text}
                      </li>
                    ))}
                  </ul>
                )}
              </section>

              {primaryMember.actor_state.compressed_memo && (
                <section className="space-y-2">
                  <div className="font-semibold">Compressed Memo</div>
                  <div className="rounded-md border border-border bg-background p-2.5 leading-relaxed text-muted-foreground">
                    {primaryMember.actor_state.compressed_memo}
                  </div>
                </section>
              )}

              <section className="space-y-2">
                <div className="font-semibold">
                  Memories (
                  {
                    (memories?.memories ?? []).filter(
                      (m) =>
                        m.kind !== "value_history" &&
                        (detail.members.length <= 1 || m.owner_id === primaryMember.character_id),
                    ).length
                  }
                  )
                </div>
                <div className="space-y-1.5">
                  {(memories?.memories ?? [])
                    .filter(
                      (m) =>
                        m.kind !== "value_history" &&
                        (detail.members.length <= 1 || m.owner_id === primaryMember.character_id),
                    )
                    .map((m) => (
                      <div
                        key={m.id}
                        className="rounded-md border border-border bg-background p-2 space-y-1"
                      >
                        <div className="text-[10px] text-muted-foreground">
                          {m.kind} · {new Date(m.occurred_at).toLocaleString()}
                        </div>
                        <div>{m.content}</div>
                      </div>
                    ))}
                </div>
              </section>
            </div>
          </ScrollArea>
        </aside>
      )}

      {/* New Direct or Group Room Dialog */}
      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Create Character Room</DialogTitle>
            <DialogDescription>
              Bind a user persona and one or more characters for a 1:1 or group chat session.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <div className="space-y-1.5">
              <Label>Room Mode</Label>
              <div className="grid grid-cols-2 gap-2">
                <Button
                  type="button"
                  size="sm"
                  variant={newMode === "direct" ? "default" : "outline"}
                  onClick={() => setNewMode("direct")}
                >
                  Direct (1:1)
                </Button>
                <Button
                  type="button"
                  size="sm"
                  variant={newMode === "group" ? "default" : "outline"}
                  onClick={() => setNewMode("group")}
                >
                  Group (Multi-Actor)
                </Button>
              </div>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="room-title">Room Title</Label>
              <Input
                id="room-title"
                value={newTitle}
                onChange={(e) => setNewTitle(e.target.value)}
                placeholder={newMode === "group" ? "Briefing Room" : "Late Night Cafe"}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="room-workdir">Working Directory (optional)</Label>
              <Input
                id="room-workdir"
                className="font-mono text-xs"
                value={newWorkDir}
                onChange={(e) => setNewWorkDir(e.target.value)}
                placeholder="Optional default folder, e.g. C:\Users\...\project or ~/notes"
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="room-persona">User Persona</Label>
              <select
                id="room-persona"
                className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
                value={newPersonaId}
                onChange={(e) => setNewPersonaId(e.target.value)}
              >
                {personas.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </select>
            </div>
            {newMode === "direct" ? (
              <div className="space-y-1.5">
                <Label htmlFor="room-character">Character</Label>
                <select
                  id="room-character"
                  className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
                  value={newCharacterId}
                  onChange={(e) => setNewCharacterId(e.target.value)}
                >
                  {characters.map((c) => (
                    <option key={c.id} value={c.id}>
                      {c.name}
                    </option>
                  ))}
                </select>
              </div>
            ) : (
              <>
                <div className="space-y-1.5">
                  <Label>Characters ({newCharacterIds.length} selected)</Label>
                  <div className="max-h-36 overflow-y-auto rounded-md border border-input bg-background p-2 space-y-1.5">
                    {characters.map((c) => {
                      const checked = newCharacterIds.includes(c.id);
                      return (
                        <label
                          key={c.id}
                          className="flex items-center gap-2 text-sm cursor-pointer select-none"
                        >
                          <input
                            type="checkbox"
                            checked={checked}
                            onChange={() => toggleGroupCharacter(c.id)}
                          />
                          <span>{c.name}</span>
                        </label>
                      );
                    })}
                  </div>
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="room-max-turns">Max Character Turns per Input</Label>
                  <Input
                    id="room-max-turns"
                    type="number"
                    min={1}
                    max={20}
                    value={newMaxTurns}
                    onChange={(e) => setNewMaxTurns(Number(e.target.value) || 3)}
                  />
                </div>
              </>
            )}
            <div className="flex justify-end gap-2 pt-2">
              <Button variant="outline" onClick={() => setCreateOpen(false)}>
                Cancel
              </Button>
              <Button
                disabled={
                  !newTitle.trim() ||
                  !newPersonaId ||
                  (newMode === "direct" ? !newCharacterId : newCharacterIds.length === 0)
                }
                onClick={() => void handleCreateRoom()}
              >
                Create Room
              </Button>
            </div>
          </div>
        </DialogContent>
      </Dialog>

      {/* Room Settings Dialog */}
      <Dialog open={editOpen} onOpenChange={setEditOpen}>
        <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>Room Settings</DialogTitle>
            <DialogDescription>
              Update title, working directory, approval policy, model &amp; reasoning effort overrides{detail?.room.mode === "group" ? ", and active group participants" : ""}.
            </DialogDescription>
          </DialogHeader>
          {(() => {
            const directCharPrefs =
              detail?.room.mode === "direct"
                ? detail.members.find((m) => m.active)?.character_snapshot.model_prefs
                : undefined;
            const effectiveRoomChatModel =
              editChatModel || directCharPrefs?.chat_model || defaultRoles.chat_model || "";
            const effectiveRoomMemoryModel =
              editMemoryModel ||
              directCharPrefs?.memory_model ||
              defaultRoles.memory_model ||
              effectiveRoomChatModel;
            const llmOptions = models.map((m) => ({
              id: m.id,
              label: m.owned_by ? `${m.id} (${m.owned_by})` : m.id,
            }));
            return (
              <div className="space-y-3">
                <div className="space-y-1.5">
                  <Label htmlFor="edit-room-title">Room Title</Label>
                  <Input
                    id="edit-room-title"
                    value={editTitle}
                    onChange={(e) => setEditTitle(e.target.value)}
                  />
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="edit-room-workdir">Working Directory (optional)</Label>
                  <Input
                    id="edit-room-workdir"
                    className="font-mono text-xs"
                    value={editWorkDir}
                    onChange={(e) => setEditWorkDir(e.target.value)}
                    placeholder="Optional default folder, e.g. C:\Users\...\project or ~/notes"
                  />
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="edit-room-approval">Approval Mode Override</Label>
                  <select
                    id="edit-room-approval"
                    className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
                    value={editApprovalOverride}
                    onChange={(e) => setEditApprovalOverride(e.target.value as "" | ApprovalMode)}
                  >
                    <option value="">Inherit Global / Character Policy</option>
                    <option value="bypass">bypass</option>
                    <option value="auto-detection">auto-detection</option>
                    <option value="request">request</option>
                  </select>
                </div>
                <div className="grid gap-3 md:grid-cols-2 pt-1">
                  <div className="space-y-1.5">
                    <Label htmlFor="edit-room-chat-model">Chat Model Override</Label>
                    <ModelSelect
                      id="edit-room-chat-model"
                      value={editChatModel}
                      options={llmOptions}
                      emptyLabel="Inherit Character / Global"
                      customPlaceholder="provider/chat-model"
                      onChange={(v) => {
                        setEditChatModel(v);
                        const nextEffChat =
                          v || directCharPrefs?.chat_model || defaultRoles.chat_model || "";
                        setEditChatReasoning((cur) =>
                          reconcileReasoningEffort(nextEffChat, cur, models),
                        );
                      }}
                    />
                  </div>
                  <div className="space-y-1.5">
                    <Label htmlFor="edit-room-chat-effort">Chat Reasoning Effort</Label>
                    <ReasoningSelect
                      id="edit-room-chat-effort"
                      modelId={effectiveRoomChatModel}
                      models={models}
                      value={editChatReasoning}
                      emptyLabel="Inherit Character / Global"
                      onChange={setEditChatReasoning}
                    />
                  </div>
                  <div className="space-y-1.5">
                    <Label htmlFor="edit-room-mem-model">Memory Model Override</Label>
                    <ModelSelect
                      id="edit-room-mem-model"
                      value={editMemoryModel}
                      options={llmOptions}
                      emptyLabel="Inherit Character / Global"
                      customPlaceholder="provider/memory-model"
                      onChange={(v) => {
                        setEditMemoryModel(v);
                        const nextEffMem =
                          v ||
                          directCharPrefs?.memory_model ||
                          defaultRoles.memory_model ||
                          effectiveRoomChatModel;
                        setEditMemoryReasoning((cur) =>
                          reconcileReasoningEffort(nextEffMem, cur, models),
                        );
                      }}
                    />
                  </div>
                  <div className="space-y-1.5">
                    <Label htmlFor="edit-room-mem-effort">Memory Reasoning Effort</Label>
                    <ReasoningSelect
                      id="edit-room-mem-effort"
                      modelId={effectiveRoomMemoryModel}
                      models={models}
                      value={editMemoryReasoning}
                      emptyLabel="Inherit Character / Global"
                      onChange={setEditMemoryReasoning}
                    />
                  </div>
                  <div className="space-y-1.5 md:col-span-2">
                    <Label htmlFor="edit-room-dec-model">Decision Model Override</Label>
                    <ModelSelect
                      id="edit-room-dec-model"
                      value={editDecisionModel}
                      options={decisionModels.map((m) => ({
                        id: m.id,
                        label: m.label ? `${m.label} (${m.id})` : m.id,
                      }))}
                      emptyLabel="Inherit Character / Global"
                      customPlaceholder="provider/decision-model"
                      onChange={setEditDecisionModel}
                    />
                  </div>
                </div>
                {detail?.room.mode === "group" && (
                  <>
                    <div className="space-y-1.5">
                      <Label>Active Characters ({editCharacterIds.length} selected)</Label>
                      <div className="max-h-36 overflow-y-auto rounded-md border border-input bg-background p-2 space-y-1.5">
                        {characters.map((c) => {
                          const checked = editCharacterIds.includes(c.id);
                          return (
                            <label
                              key={c.id}
                              className="flex items-center gap-2 text-sm cursor-pointer select-none"
                            >
                              <input
                                type="checkbox"
                                checked={checked}
                                onChange={() => toggleEditCharacter(c.id)}
                              />
                              <span>{c.name}</span>
                            </label>
                          );
                        })}
                      </div>
                    </div>
                    <div className="space-y-1.5">
                      <Label htmlFor="edit-room-max-turns">Max Character Turns per Input</Label>
                      <Input
                        id="edit-room-max-turns"
                        type="number"
                        min={1}
                        max={20}
                        value={editMaxTurns}
                        onChange={(e) => setEditMaxTurns(Number(e.target.value) || 3)}
                      />
                    </div>
                  </>
                )}
                <div className="flex justify-end gap-2 pt-2">
                  <Button variant="outline" onClick={() => setEditOpen(false)}>
                    Cancel
                  </Button>
                  <Button
                    disabled={
                      !editTitle.trim() ||
                      (detail?.room.mode === "group" && editCharacterIds.length === 0)
                    }
                    onClick={() => void handleSaveRoomSettings()}
                  >
                    Save Changes
                  </Button>
                </div>
              </div>
            );
          })()}
        </DialogContent>
      </Dialog>

      <LoomArtifactDrawer
        client={client}
        artifactId={openArtifactId}
        onClose={() => setOpenArtifactId(null)}
      />
    </div>
  );
}
