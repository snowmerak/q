import { describe, expect, it } from "vitest";
import type { ModelInfo } from "@/client/types";
import { reasoningOptions, reconcileReasoningEffort } from "./reasoning-select";

const MODELS: ModelInfo[] = [
  {
    id: "openai/o4-mini",
    capabilities: {
      reasoning: {
        supported: true,
        control: "effort",
        supported_efforts: ["low", "medium", "high"],
        default_effort: "medium",
      },
    },
  },
  {
    id: "openrouter/hermes-thinking",
    capabilities: {
      reasoning: {
        supported: true,
        control: "effort",
      },
    },
  },
  {
    id: "anthropic/claude-fixed",
    capabilities: {
      reasoning: {
        supported: true,
        control: "fixed",
        mandatory: true,
      },
    },
  },
  {
    id: "openai/gpt-4.1-mini",
    capabilities: {
      tool_calling: true,
    },
  },
];

describe("reasoningOptions", () => {
  it("returns advertised supported_efforts for effort-controlled models", () => {
    expect(reasoningOptions("openai/o4-mini", "", MODELS)).toEqual(["low", "medium", "high"]);
  });

  it("falls back to common efforts when effort control omits supported_efforts", () => {
    expect(reasoningOptions("openrouter/hermes-thinking", "", MODELS)).toEqual([
      "minimal",
      "low",
      "medium",
      "high",
      "xhigh",
      "max",
      "ultra",
    ]);
  });

  it("returns empty options for fixed or unsupported models when no current value exists", () => {
    expect(reasoningOptions("anthropic/claude-fixed", "", MODELS)).toEqual([]);
    expect(reasoningOptions("openai/gpt-4.1-mini", "", MODELS)).toEqual([]);
  });

  it("preserves a stale current value so the user can still inspect or clear it", () => {
    expect(reasoningOptions("openai/gpt-4.1-mini", "high", MODELS)).toEqual(["high"]);
  });
});

describe("reconcileReasoningEffort", () => {
  it("keeps effort when valid for the next model and clears it when invalid", () => {
    expect(reconcileReasoningEffort("openai/o4-mini", "high", MODELS)).toBe("high");
    expect(reconcileReasoningEffort("openai/gpt-4.1-mini", "high", MODELS)).toBe("");
    expect(reconcileReasoningEffort("anthropic/claude-fixed", "medium", MODELS)).toBe("");
  });
});
