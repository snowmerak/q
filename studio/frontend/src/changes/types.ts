export type ChangedFile = { path: string; old_path?: string; status: string };
export type ChangeSnapshot = { root: string; files: ChangedFile[] };
export type ChangeDetail = { root: string; file: ChangedFile; detail: { sections: { title: string; patch: string; truncated: boolean }[] } };
export type Proposal = { type: string; scope?: string; summary: string; body?: string[]; files?: string[] };
export type Progress = { stage: string; message: string };
export type CommitReview = { id: string; root: string; proposals: Proposal[]; auto_staged: boolean; progress: Progress[]; created_at: string; updated_at: string };
export type CommitResult = { status: string; result: { messages: string[]; auto_staged: boolean; used_fallback: boolean; split: boolean }; push_error?: string };
