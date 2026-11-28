import type { SSEEvent } from "./types";

export interface SSEParseState {
  buffer: string;
}

export function createSSEParseState(): SSEParseState {
  return { buffer: "" };
}

/**
 * Incrementally parses text chunks adhering to the text/event-stream wire format,
 * ignoring heartbeat comment frames (`: keepalive`) and returning complete events.
 */
export function parseSSEChunk(state: SSEParseState, chunk: string): SSEEvent[] {
  state.buffer += chunk.replace(/\r\n/g, "\n");
  const events: SSEEvent[] = [];

  for (;;) {
    const boundary = state.buffer.indexOf("\n\n");
    if (boundary === -1) {
      break;
    }
    const rawFrame = state.buffer.slice(0, boundary);
    state.buffer = state.buffer.slice(boundary + 2);

    if (!rawFrame.trim()) {
      continue;
    }

    let id: string | undefined;
    let eventName = "message";
    const dataLines: string[] = [];

    for (const line of rawFrame.split("\n")) {
      if (line.startsWith(":")) {
        continue;
      }
      const colonIdx = line.indexOf(":");
      const field = colonIdx === -1 ? line : line.slice(0, colonIdx);
      let value = colonIdx === -1 ? "" : line.slice(colonIdx + 1);
      if (value.startsWith(" ")) {
        value = value.slice(1);
      }
      switch (field) {
        case "id":
          id = value;
          break;
        case "event":
          eventName = value || "message";
          break;
        case "data":
          dataLines.push(value);
          break;
      }
    }

    if (dataLines.length === 0) {
      continue;
    }

    const rawData = dataLines.join("\n");
    let parsedData: unknown = rawData;
    try {
      parsedData = JSON.parse(rawData);
    } catch {
      // Keep raw string when payload is plain text.
    }

    events.push({
      ...(id !== undefined ? { id } : {}),
      event: eventName,
      data: parsedData,
    });
  }

  return events;
}

export interface RoomStreamOptions {
  baseUrl?: string;
  fetchFn?: typeof fetch;
  initialLastEventId?: string;
  onEvent: (event: SSEEvent) => void;
  onReconnect?: () => void;
  onError?: (err: unknown) => void;
}

/**
 * Subscribes to `/api/v1/rooms/{id}/stream` using fetch + ReadableStream so
 * `Last-Event-ID` can be attached cleanly on reconnects. Returns an unsubscribe fn.
 */
export function subscribeRoomStream(roomId: string, options: RoomStreamOptions): () => void {
  const controller = new AbortController();
  const baseUrl = (options.baseUrl ?? "").replace(/\/+$/, "");
  const fetchFn = options.fetchFn ?? ((input, init) => fetch(input, init));
  let lastEventId = options.initialLastEventId;
  let attempt = 0;

  void (async () => {
    while (!controller.signal.aborted) {
      try {
        const headers: Record<string, string> = {
          Accept: "text/event-stream",
        };
        if (lastEventId) {
          headers["Last-Event-ID"] = lastEventId;
        }
        const resp = await fetchFn(`${baseUrl}/api/v1/rooms/${encodeURIComponent(roomId)}/stream`, {
          method: "GET",
          headers,
          signal: controller.signal,
        });
        if (!resp.ok || !resp.body) {
          throw new Error(`SSE HTTP ${resp.status}`);
        }
        if (attempt > 0) {
          options.onReconnect?.();
        }
        attempt = 0;

        const reader = resp.body.getReader();
        const decoder = new TextDecoder();
        const state = createSSEParseState();
        while (!controller.signal.aborted) {
          const { value, done } = await reader.read();
          if (done) {
            break;
          }
          const chunk = decoder.decode(value, { stream: true });
          for (const ev of parseSSEChunk(state, chunk)) {
            if (ev.id) {
              lastEventId = ev.id;
            }
            options.onEvent(ev);
          }
        }
      } catch (err) {
        if (controller.signal.aborted) {
          return;
        }
        options.onError?.(err);
      }
      attempt++;
      const delayMs = Math.min(8000, 400 * 2 ** Math.min(attempt - 1, 4));
      await new Promise((resolve) => {
        const timer = setTimeout(resolve, delayMs);
        controller.signal.addEventListener(
          "abort",
          () => {
            clearTimeout(timer);
            resolve(undefined);
          },
          { once: true },
        );
      });
    }
  })();

  return () => controller.abort();
}
