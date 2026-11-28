import type { MessageBubble, RoomMember, ToolInvocation } from "@/client/types";

export interface MessageActivity {
  thinking: string;
  invocations: ToolInvocation[];
}

export interface TimelineActivity {
  byMessageId: Record<string, MessageActivity>;
  liveInvocations: ToolInvocation[];
}

export function parseMessageThinking(metadataJson?: string): string {
  if (!metadataJson) return "";
  try {
    const parsed = JSON.parse(metadataJson) as { thinking?: unknown } | null;
    if (parsed && typeof parsed.thinking === "string" && parsed.thinking.trim() !== "") {
      return parsed.thinking;
    }
  } catch {
    return "";
  }
  return "";
}

export function isMessageCompacted(msg: MessageBubble, members: RoomMember[]): boolean {
  if (msg.role === "character") {
    const owner = members.find((m) => m.character_id === msg.author_id);
    if (owner) {
      const seq = owner.actor_state.compacted_through_seq ?? 0;
      return seq > 0 && msg.seq <= seq;
    }
  }
  const active = members.filter((m) => m.active);
  if (active.length === 0) return false;
  const minSeq = Math.min(...active.map((m) => m.actor_state.compacted_through_seq ?? 0));
  return minSeq > 0 && msg.seq <= minSeq;
}

function compareInvocations(a: ToolInvocation, b: ToolInvocation): number {
  if (a.created_at !== b.created_at) return a.created_at - b.created_at;
  return a.id.localeCompare(b.id);
}

export function mergeToolInvocations(
  existing: ToolInvocation[],
  incoming: ToolInvocation[],
): ToolInvocation[] {
  const byId = new Map<string, ToolInvocation>();
  for (const inv of existing) {
    byId.set(inv.id, inv);
  }
  for (const inv of incoming) {
    byId.set(inv.id, inv);
  }
  return Array.from(byId.values()).sort(compareInvocations);
}

export function groupTimelineActivity(
  messages: MessageBubble[],
  members: RoomMember[],
  invocations: ToolInvocation[],
  activeTurnId?: string,
): TimelineActivity {
  const byMessageId: Record<string, MessageActivity> = {};
  const compactedMessageIds = new Set<string>();
  const compactedTurnActors = new Set<string>();
  const charMessagesByTurnActor = new Map<string, MessageBubble[]>();
  let maxCompactedCreatedAt = 0;

  for (const msg of messages) {
    const compacted = isMessageCompacted(msg, members);
    if (compacted) {
      compactedMessageIds.add(msg.id);
      if (msg.created_at > maxCompactedCreatedAt) {
        maxCompactedCreatedAt = msg.created_at;
      }
      if (msg.role === "character" && msg.turn_id) {
        compactedTurnActors.add(`${msg.turn_id}\0${msg.author_id}`);
      }
    } else {
      const thinking = parseMessageThinking(msg.metadata_json);
      if (thinking) {
        byMessageId[msg.id] = { thinking, invocations: [] };
      }
    }

    if (msg.role === "character" && msg.turn_id) {
      const key = `${msg.turn_id}\0${msg.author_id}`;
      const list = charMessagesByTurnActor.get(key) ?? [];
      list.push(msg);
      charMessagesByTurnActor.set(key, list);
    }
  }

  const deduped = mergeToolInvocations([], invocations);
  const liveInvocations: ToolInvocation[] = [];

  for (const inv of deduped) {
    const key = `${inv.turn_id}\0${inv.actor_id}`;
    const candidates = charMessagesByTurnActor.get(key);
    if (candidates && candidates.length > 0) {
      const target =
        candidates.find((m) => m.created_at >= inv.created_at) ??
        candidates[candidates.length - 1];
      if (!compactedMessageIds.has(target.id)) {
        if (!byMessageId[target.id]) {
          byMessageId[target.id] = { thinking: "", invocations: [] };
        }
        byMessageId[target.id].invocations.push(inv);
      }
      continue;
    }

    if (activeTurnId && inv.turn_id === activeTurnId) {
      liveInvocations.push(inv);
      continue;
    }
    if (compactedTurnActors.has(key)) {
      continue;
    }
    if (maxCompactedCreatedAt > 0 && inv.created_at <= maxCompactedCreatedAt) {
      continue;
    }
    liveInvocations.push(inv);
  }

  return { byMessageId, liveInvocations };
}
