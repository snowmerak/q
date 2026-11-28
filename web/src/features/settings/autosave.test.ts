import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createAutoSaver, type AutoSaveState } from "./autosave";

describe("createAutoSaver", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("coalesces rapid edits into a single save after the debounce delay", async () => {
    const saved: number[] = [];
    const states: AutoSaveState[] = [];
    const saver = createAutoSaver<number>({
      delayMs: 500,
      save: async (val) => {
        saved.push(val);
      },
      onStateChange: (s) => states.push(s),
    });

    saver.schedule(1);
    vi.advanceTimersByTime(200);
    saver.schedule(2);
    vi.advanceTimersByTime(200);
    saver.schedule(3);
    expect(saved).toEqual([]);

    await vi.advanceTimersByTimeAsync(500);
    expect(saved).toEqual([3]);
    expect(states[states.length - 1]).toBe("saved");
  });

  it("serializes in-flight saves and persists newer edits made during save", async () => {
    const saved: string[] = [];
    let resolveFirst!: () => void;
    const firstGate = new Promise<void>((r) => {
      resolveFirst = r;
    });

    const saver = createAutoSaver<string>({
      delayMs: 100,
      save: async (val) => {
        saved.push(`start:${val}`);
        if (val === "first") {
          await firstGate;
        }
        saved.push(`done:${val}`);
      },
    });

    saver.schedule("first");
    await vi.advanceTimersByTimeAsync(100);
    expect(saved).toEqual(["start:first"]);

    // User types while "first" save is still in flight.
    saver.schedule("second");
    await vi.advanceTimersByTimeAsync(100);
    // Single-flight: "second" must wait for "first" to finish.
    expect(saved).toEqual(["start:first"]);

    resolveFirst();
    await Promise.resolve();
    await Promise.resolve();
    await vi.advanceTimersByTimeAsync(0);
    expect(saved).toEqual(["start:first", "done:first", "start:second", "done:second"]);
  });

  it("reports error state and recovers on subsequent valid edit", async () => {
    const states: AutoSaveState[] = [];
    let shouldFail = true;
    const saver = createAutoSaver<string>({
      delayMs: 100,
      save: async () => {
        if (shouldFail) throw new Error("bad regex");
      },
      onStateChange: (s) => states.push(s),
    });

    saver.schedule("bad");
    await vi.advanceTimersByTimeAsync(100);
    expect(states[states.length - 1]).toBe("error");

    shouldFail = false;
    saver.schedule("good");
    await vi.advanceTimersByTimeAsync(100);
    expect(states[states.length - 1]).toBe("saved");
  });

  it("flushes pending changes immediately on dispose without spurious calls when clean", async () => {
    const saved: string[] = [];
    const cleanSaver = createAutoSaver<string>({
      delayMs: 500,
      save: async (v) => {
        saved.push(v);
      },
    });
    cleanSaver.dispose();
    expect(saved).toEqual([]);

    const dirtySaver = createAutoSaver<string>({
      delayMs: 500,
      save: async (v) => {
        saved.push(v);
      },
    });
    dirtySaver.schedule("pending-on-unmount");
    dirtySaver.dispose();
    await Promise.resolve();
    expect(saved).toEqual(["pending-on-unmount"]);
  });
});
