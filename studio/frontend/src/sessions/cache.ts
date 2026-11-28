import type { RegisteredSessionTree, StudioProject } from './types';

type SessionTreeCache = {
  projects: StudioProject[];
  sessions: RegisteredSessionTree[];
};

let overviewCache: RegisteredSessionTree[] | null = null;
let sessionTreeCache: SessionTreeCache | null = null;

export function readOverviewCache(): RegisteredSessionTree[] | null {
  return overviewCache;
}

export function writeOverviewCache(sessions: RegisteredSessionTree[]) {
  overviewCache = sessions;
}

export function readSessionTreeCache(): SessionTreeCache | null {
  return sessionTreeCache;
}

export function writeSessionTreeCache(value: SessionTreeCache) {
  sessionTreeCache = value;
}

export function invalidateSessionCaches() {
  overviewCache = null;
  sessionTreeCache = null;
}
