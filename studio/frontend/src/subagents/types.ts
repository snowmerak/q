export type AgentConnection = { preset?: string; command?: string; args?: string[]; env?: Record<string, string>; auth_method?: string; disabled?: boolean; library?: boolean };
type ACPSelection = { agent_model?: string; agent_reasoning_effort?: string };
export type ACPExecutionTarget = ACPSelection & { agent: string; model?: never; reasoning_effort?: never; default?: never };
export type ExecutionTarget =
  | { model: string; reasoning_effort?: string; agent?: never; default?: never }
  | { default: true; reasoning_effort?: string; model?: never; agent?: never }
  | ACPExecutionTarget;
export type AgentDefinition = { name: string; description: string; source: string; kind: string; role: string; connection?: string; target?: ExecutionTarget; external_only?: boolean; available: boolean; mutates_workspace: boolean; system_prompt: string; tools: string[]; delegates: string[] };
export type Profile = ACPSelection & { version: number; name: string; description?: string; kind: string; role?: string; agent?: string; target?: ExecutionTarget; system_prompt: string; mutates_workspace?: boolean; tools: string[]; delegates: string[] };
export type ProfileEntry = { profile: Profile; scope: string; path: string; revision: string; shadowed: boolean; error?: string; effective_target?: ExecutionTarget; _originalScope?: string };
export type AgentResponse = { workspace_root: string; config_path: string; default_model: string; connections: Record<string, AgentConnection>; bindings: Record<string, string>; external_roles: string[]; native_roles: string[]; builtins: AgentDefinition[]; profiles: ProfileEntry[]; tool_names: string[] };
