import { describe, expect, it } from "vitest";
import type { MessageBubble, RoomMember, ToolInvocation } from "@/client/types";
import { groupTimelineActivity, isMessageCompacted, parseMessageThinking } from "./timelineActivity";

function makeMember(characterId: string, compactedThroughSeq = 0, active = true): RoomMember {
  return {
    room_id: "r1",
    character_id: characterId,
    base_speed: 50,
    active,
    joined_at: 1,
    character_snapshot: {
      id: characterId,
      name: characterId,
      version: 1,
      created_at: 1,
      updated_at: 1,
    },
    actor_state: {
      self: { valence: 0, activation: 0, stressed: false },
      compacted_through_seq: compactedThroughSeq,
      values_revision: 1,
    },
  };
}

function makeMessage(partial: Partial<MessageBubble> & Pick<MessageBubble, "id" | "seq" | "turn_id">): MessageBubble {
  return {
    room_id: "r1",
    role: "character",
    author_id: "char-a",
    author_name: "Alice",
    content: "Hello",
    observed_by: ["user", "char-a"],
    created_at: partial.seq * 100,
    ...partial,
  };
}

function makeInvocation(
  partial: Partial<ToolInvocation> & Pick<ToolInvocation, "id" | "turn_id" | "actor_id" | "created_at">,
): ToolInvocation {
  return {
    room_id: "r1",
    call_id: `call-${partial.id}`,
    tool_name: "read_file",
    tool_source: "builtin",
    args_json: "{}",
    gate_mode: "auto-detection",
    status: "completed",
    updated_at: partial.created_at + 1,
    ...partial,
  };
}

describe("timelineActivity", () => {
  it("parses thinking metadata safely", () => {
    expect(parseMessageThinking(undefined)).toBe("");
    expect(parseMessageThinking("not json")).toBe("");
    expect(parseMessageThinking('{"usage":{"total_tokens":5}}')).toBe("");
    expect(parseMessageThinking('{"thinking":"  step 1  "}')).toBe("  step 1  ");
  });

  it("attaches uncompacted thinking and multi-round tool calls to their owning message", () => {
    const members = [makeMember("char-a", 0)];
    const msgs = [
      makeMessage({ id: "m1", seq: 1, turn_id: "t1", role: "user", author_id: "user", created_at: 100 }),
      makeMessage({
        id: "m2",
        seq: 2,
        turn_id: "t1",
        created_at: 200,
        metadata_json: JSON.stringify({ thinking: "round1 thought\n\nround2 thought" }),
      }),
    ];
    const invs = [
      makeInvocation({ id: "i2", turn_id: "t1", actor_id: "char-a", created_at: 180, tool_name: "grep" }),
      makeInvocation({ id: "i1", turn_id: "t1", actor_id: "char-a", created_at: 150, tool_name: "read_file" }),
    ];

    const activity = groupTimelineActivity(msgs, members, invs);
    expect(activity.byMessageId["m2"]).toBeDefined();
    expect(activity.byMessageId["m2"].thinking).toBe("round1 thought\n\nround2 thought");
    expect(activity.byMessageId["m2"].invocations.map((i) => i.id)).toEqual(["i1", "i2"]);
    expect(activity.liveInvocations).toEqual([]);
  });

  it("hides thinking and tool calls once compacted per actor in group rooms", () => {
    const members = [makeMember("char-a", 3), makeMember("char-b", 1)];
    const mA = makeMessage({
      id: "mA",
      seq: 2,
      turn_id: "t1",
      author_id: "char-a",
      created_at: 200,
      metadata_json: JSON.stringify({ thinking: "old A thought" }),
    });
    const mB = makeMessage({
      id: "mB",
      seq: 3,
      turn_id: "t1",
      author_id: "char-b",
      created_at: 300,
      metadata_json: JSON.stringify({ thinking: "still visible B thought" }),
    });
    expect(isMessageCompacted(mA, members)).toBe(true);
    expect(isMessageCompacted(mB, members)).toBe(false);

    const invs = [
      makeInvocation({ id: "iA", turn_id: "t1", actor_id: "char-a", created_at: 150 }),
      makeInvocation({ id: "iB", turn_id: "t1", actor_id: "char-b", created_at: 250 }),
    ];

    const activity = groupTimelineActivity([mA, mB], members, invs);
    expect(activity.byMessageId["mA"]).toBeUndefined();
    expect(activity.byMessageId["mB"].thinking).toBe("still visible B thought");
    expect(activity.byMessageId["mB"].invocations.map((i) => i.id)).toEqual(["iB"]);
  });

  it("keeps live unattached tool calls for the active turn while dropping stale pre-compaction ones", () => {
    const members = [makeMember("char-a", 2)];
    const msgs = [
      makeMessage({ id: "m1", seq: 1, turn_id: "t1", role: "user", author_id: "user", created_at: 100 }),
      makeMessage({ id: "m2", seq: 2, turn_id: "t1", author_id: "char-a", created_at: 200 }),
      makeMessage({ id: "m3", seq: 3, turn_id: "t2", role: "user", author_id: "user", created_at: 300 }),
    ];
    const invs = [
      makeInvocation({ id: "stale", turn_id: "t0", actor_id: "char-a", created_at: 150 }),
      makeInvocation({
        id: "live",
        turn_id: "t2",
        actor_id: "char-a",
        created_at: 320,
        status: "running",
      }),
    ];

    const activity = groupTimelineActivity(msgs, members, invs, "t2");
    expect(activity.liveInvocations.map((i) => i.id)).toEqual(["live"]);
  });
});
