import { describe, expect, it } from "vitest";
import { QAPIError, QClient } from "./api";
import { createSSEParseState, parseSSEChunk } from "./sse";

describe("QClient", () => {
  it("parses structured API error JSON payloads into QAPIError", async () => {
    const client = new QClient({
      baseUrl: "http://127.0.0.1:17070",
      fetchFn: async () =>
        new Response(
          JSON.stringify({
            error: {
              code: "conflict",
              message: "request_id already exists with different input",
              retryable: false,
            },
          }),
          { status: 409, headers: { "Content-Type": "application/json" } },
        ),
    });

    await expect(
      client.submitTurn("room-1", { request_id: "req-1", text: "hi" }),
    ).rejects.toMatchObject({
      name: "QAPIError",
      status: 409,
      code: "conflict",
      retryable: false,
    });
  });

  it("handles 204 No Content cleanly on deletes", async () => {
    const client = new QClient({
      fetchFn: async () => new Response(null, { status: 204 }),
    });
    await expect(client.deleteRoom("r1")).resolves.toBeUndefined();
    expect(QAPIError).toBeDefined();
  });
});

describe("parseSSEChunk", () => {
  it("handles split chunks, ignores keepalive comments, and decodes JSON data", () => {
    const state = createSSEParseState();
    const first = parseSSEChunk(state, ": connected\n\nid: 7\nevent: bubble.del");
    expect(first).toHaveLength(0);

    const second = parseSSEChunk(
      state,
      'ta\ndata: {"turn_id":"t1","delta":"hello"}\n\n: keepalive\n\nid: 8\nevent: turn.completed\ndata: {"turn_id":"t1","status":"completed"}\n\n',
    );
    expect(second).toEqual([
      {
        id: "7",
        event: "bubble.delta",
        data: { turn_id: "t1", delta: "hello" },
      },
      {
        id: "8",
        event: "turn.completed",
        data: { turn_id: "t1", status: "completed" },
      },
    ]);
  });
});
