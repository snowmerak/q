import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

export interface ModelSelectOption {
  id: string;
  label?: string;
}

interface ModelSelectProps {
  id?: string;
  value: string;
  onChange: (value: string) => void;
  options: ModelSelectOption[];
  emptyLabel?: string;
  allowEmpty?: boolean;
  customPlaceholder?: string;
}

const CUSTOM_SENTINEL = "__q_custom_model_id__";

export function ModelSelect({
  id,
  value,
  onChange,
  options,
  emptyLabel = "— Not configured —",
  allowEmpty = true,
  customPlaceholder = "provider/model-id",
}: ModelSelectProps) {
  const [customMode, setCustomMode] = useState<boolean>(false);

  const uniqueOptions: ModelSelectOption[] = [];
  const seen = new Set<string>();
  for (const opt of options) {
    if (!opt.id || seen.has(opt.id)) continue;
    seen.add(opt.id);
    uniqueOptions.push(opt);
  }
  const hasCurrent = value !== "" && !seen.has(value);

  if (customMode) {
    return (
      <div className="flex items-center gap-1.5">
        <Input
          id={id}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder={customPlaceholder}
        />
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => setCustomMode(false)}
        >
          List
        </Button>
      </div>
    );
  }

  return (
    <select
      id={id}
      className="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
      value={value}
      onChange={(e) => {
        if (e.target.value === CUSTOM_SENTINEL) {
          setCustomMode(true);
          return;
        }
        onChange(e.target.value);
      }}
    >
      {allowEmpty && <option value="">{emptyLabel}</option>}
      {hasCurrent && (
        <option value={value}>{value} (configured)</option>
      )}
      {uniqueOptions.map((opt) => (
        <option key={opt.id} value={opt.id}>
          {opt.label && opt.label !== opt.id ? `${opt.label} (${opt.id})` : opt.id}
        </option>
      ))}
      <option value={CUSTOM_SENTINEL}>Custom model ID…</option>
    </select>
  );
}
