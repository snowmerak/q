import type { ChangedFile } from '../changes/types';
export type FileEntry = { name: string; path: string; directory: boolean; symlink: boolean; unavailable: boolean };
export type FileListing = { workspace_root: string; path: string; entries: FileEntry[]; next_offset: number };
export type FileContent = { workspace_root: string; path: string; content: string; size: number; binary: boolean; missing: boolean; truncated: boolean };
export type FileChanges = { workspace_root: string; available: boolean; reason?: string; files: ChangedFile[] };
export type FileDiff = { workspace_root: string; path: string; available: boolean; reason?: string; sections: { title: string; patch: string; truncated: boolean }[] };
