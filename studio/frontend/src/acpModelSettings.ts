export type ACPModelOption = { id: string; name: string; group?: string };

export type ACPModelSettings = {
  supported: boolean;
  current_model: string;
  models: ACPModelOption[];
  reasoning_supported: boolean;
  current_reasoning_effort: string;
  reasoning_efforts: ACPModelOption[];
};
