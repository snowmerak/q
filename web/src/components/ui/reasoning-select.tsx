import type { ModelInfo } from "@/client/types";

const COMMON_EFFORTS = ["minimal", "low", "medium", "high", "xhigh", "max", "ultra"];

export function findDiscoveredModel(modelId: string, models: ModelInfo[]): ModelInfo | undefined {
  const clean = modelId.trim();
  if (!clean) return undefined;
  return models.find((m) => m.id === clean);
}

/**
 * Returns supported reasoning_effort options strictly from the selected model's
 * advertised `capabilities.reasoning` metadata (mirroring Q v1's reasoning.ts).
 */
export function reasoningOptions(modelId: string, current: string, models: ModelInfo[]): string[] {
  const discovered = findDiscoveredModel(modelId, models);
  const rc = discovered?.capabilities?.reasoning;
  let values: string[] = [];
  if (rc?.supported && rc.control === "effort") {
    values = rc.supported_efforts && rc.supported_efforts.length > 0
      ? [...rc.supported_efforts]
      : [...COMMON_EFFORTS];
  }
  const cleanCurrent = current.trim();
  if (cleanCurrent && !values.includes(cleanCurrent)) {
    values.push(cleanCurrent);
  }
  return [...new Set(values)];
}

/**
 * Keeps `currentEffort` when switching models only if it remains valid for `nextModelId`;
 * otherwise clears it back to `""` (provider / inherited default).
 */
export function reconcileReasoningEffort(
  nextModelId: string,
  currentEffort: string,
  models: ModelInfo[],
): string {
  const clean = currentEffort.trim();
  if (!clean) return "";
  const supported = reasoningOptions(nextModelId, "", models);
  return supported.includes(clean) ? clean : "";
}

export function reasoningCapabilitySummary(modelId: string, models: ModelInfo[]): string | null {
  const discovered = findDiscoveredModel(modelId, models);
  if (!discovered) {
    return modelId.trim() ? "Model metadata not discovered yet" : null;
  }
  const rc = discovered.capabilities?.reasoning;
  if (!rc?.supported) {
    return "Model does not advertise reasoning effort control";
  }
  if (rc.control && rc.control !== "effort") {
    return `Model uses ${rc.control} reasoning control`;
  }
  if (rc.default_effort) {
    return `Default effort: ${rc.default_effort}`;
  }
  return null;
}

interface ReasoningSelectProps {
  id?: string;
  modelId: string;
  models: ModelInfo[];
  value: string;
  onChange: (value: string) => void;
  emptyLabel?: string;
  showHint?: boolean;
}

export function ReasoningSelect({
  id,
  modelId,
  models,
  value,
  onChange,
  emptyLabel = "Provider default",
  showHint = true,
}: ReasoningSelectProps) {
  const options = reasoningOptions(modelId, value, models);
  const discovered = findDiscoveredModel(modelId, models);
  const defaultEffort = discovered?.capabilities?.reasoning?.default_effort;
  const disabled = !modelId.trim() || (options.length === 0 && !value.trim());
  const hint = showHint ? reasoningCapabilitySummary(modelId, models) : null;

  return (
    <div className="space-y-1">
      <select
        id={id}
        disabled={disabled}
        className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm disabled:opacity-60"
        value={value}
        onChange={(e) => onChange(e.target.value)}
      >
        <option value="">{emptyLabel}</option>
        {options.map((effort) => (
          <option key={effort} value={effort}>
            {defaultEffort === effort ? `${effort} (default)` : effort}
          </option>
        ))}
      </select>
      {hint && <p className="text-[11px] text-muted-foreground">{hint}</p>}
    </div>
  );
}
