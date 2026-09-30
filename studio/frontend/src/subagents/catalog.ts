import type { AgentResponse } from './types';

export function prepareAgents(value: AgentResponse) {
  value.connections ||= {};
  value.bindings ||= {};
  value.external_roles ||= [];
  value.native_roles ||= [];
  value.builtins = (value.builtins || []).map((definition) => ({
    ...definition,
    tools: definition.tools || [],
    delegates: definition.delegates || []
  }));
  value.profiles = (value.profiles || []).map((entry) => ({ ...entry, profile: { ...entry.profile, tools: entry.profile.tools || [], delegates: entry.profile.delegates || [] }, _originalScope: entry.scope }));
  value.tool_names ||= [];
  for (const connection of Object.values(value.connections)) {
    connection.args ||= [];
    connection.env ||= {};
  }
  return value;
}
