export type SessionSummary = {
  session_id: string;
  run_id?: string;
  title?: string;
  updated_at: string;
};
export type ToolCall = { id: string; function?: { name?: string; arguments?: string } };
export type TokenUsage = { input_tokens: number; cached_tokens?: number; output_tokens: number };
export type Message = {
  role: string;
  content?: string;
  name?: string;
  tool_call_id?: string;
  tool_calls?: ToolCall[];
  usage?: TokenUsage;
};
export type SessionDetail = {
  workspace_root: string;
  session: SessionSummary;
  transcript: Message[];
  active_task?: { objective: string; completion_criteria?: string[]; started_at: string };
};
export type RunEvent = {
  usage?: TokenUsage;
  type: string;
  run_id?: string;
  session_id?: string;
  kind?: string;
  start?: boolean;
  content?: string;
  detail?: string;
  name?: string;
  role?: string;
  agent?: string;
  task_id?: string;
  parent_id?: string;
  action?: string;
  is_error?: boolean;
  question?: string;
  context?: string;
  outcome?: string;
  call_id?: string;
  context_used?: number;
  context_size?: number;
};
export type RunQuestion = { call_id: string; question: string; context?: string; choices?: { id: string; label: string; description?: string }[] };
export type RunSnapshot = {
  id: string;
  session_id: string;
  status: string;
  outcome?: string;
  error?: string;
  pending_question?: RunQuestion;
  context_used?: number;
  context_size?: number;
  cursor: number;
};
export type RunEventEnvelope = { cursor: number; at: string; event: RunEvent };
export type RunPage = { run: RunSnapshot; events: RunEventEnvelope[]; next_cursor: number };
export type ChangeRequest = {
  id: string;
  repository_root: string;
  worktree_path?: string;
  base_ref: string;
  base_commit: string;
  head_ref: string;
  head_commit?: string;
  merged_commit?: string;
  status: string;
};
export type DelegationNode = {
  bookmark: { invocation_id: string; agent: string; prompt: string; working_directory?: string; task_id?: string; parent_id?: string };
  state?: { status: string; kind?: string; task_id?: string; parent_id?: string; model?: string; running_call?: { name: string; call_id: string }; unknown_tools?: { name: string; call_id: string }[]; change_request?: ChangeRequest };
  transcript?: Message[];
  children?: DelegationNode[];
  issue?: string;
};
export type DelegationSession = { node: DelegationNode; kind: string; run?: { id: string; status: string; error?: string } };
export type FlatDelegation = DelegationNode & { depth: number; path: string };
export type RegisteredSessionTree = {
  registration_id: string;
  workspace_root: string;
  project_id?: string;
  project_name?: string;
  session: SessionSummary;
  registered_at: string;
  delegations?: DelegationNode[];
  issue?: string;
};
export type StudioProject = {
  id: string;
  name: string;
  workspace_roots: string[];
  created_at: string;
  updated_at: string;
};
