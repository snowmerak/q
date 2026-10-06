import type { ModelOption } from './types';

export function reasoningOptions(model: string, current: string, modelOptions: ModelOption[]): string[] {
  const discovered = modelOptions.find((candidate) => candidate.id === model);
  const common = ['minimal', 'low', 'medium', 'high', 'xhigh', 'max', 'ultra'];
  const values = discovered?.reasoning_control === 'effort' && discovered.reasoning_efforts?.length
    ? [...discovered.reasoning_efforts]
    : model.startsWith('group/') || current ? common : [];
  if (current && !values.includes(current)) values.push(current);
  return [...new Set(values)];
}
