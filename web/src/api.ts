// Typed wrappers around the agentq REST API.
// Base URL is relative -- Vite proxies /api to :8080 in dev;
// in production the Go server serves both /api and the static bundle.

export interface SessionSummary {
  id: string;
  user_id: string;
  prompt: string;
  parent_session_id?: string;
  status: string;
  artifact_count: number;
  created_at: string;
  updated_at: string;
}

export interface Artifact {
  agent_name: string;
  content: string;
  content_type: string;
  origin_session_id?: string;
  created_at: string;
}

export interface Session extends SessionSummary {
  artifacts: Artifact[];
}

export interface Agent {
  name: string;
  queue: string;
  description: string;
  prompt_file?: string;
  cmd?: string;
  approval_suffix?: string;
}

export interface QueueInfo {
  name: string;
  pending: number;
  total: number;
}

export interface HumanReviewRequest {
  session_id: string;
  session_uri: string;
  reply_queue: string;
  agent_name: string;
  artifact_content: string;
  context_summary: string;
}

export interface ReviewItem {
  task_id: string;
  at: string;
  request: HumanReviewRequest;
}

export interface SubmitRequest {
  prompt: string;
  user_id?: string;
  continue_from?: string;
  repo?: string;
  compact?: boolean;
}

export interface SubmitResponse {
  session_id: string;
  session_uri: string;
  status: string;
}

export interface AddAgentRequest {
  name: string;
  queue: string;
  description: string;
  cmd?: string;
  approval_suffix?: string;
}

async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, init);
  if (!res.ok) {
    const text = await res.text().catch(() => res.statusText);
    throw new Error(`${res.status} ${text}`);
  }
  return res.json() as Promise<T>;
}

// Sessions
export const listSessions = (status?: string, limit?: number) => {
  const params = new URLSearchParams();
  if (status) params.set('status', status);
  if (limit) params.set('limit', String(limit));
  const qs = params.toString();
  return apiFetch<SessionSummary[]>(`/api/v1/sessions${qs ? '?' + qs : ''}`);
};

export const getSession = (id: string) =>
  apiFetch<Session>(`/api/v1/sessions/${id}`);

export const getSessionChain = (id: string) =>
  apiFetch<Session[]>(`/api/v1/sessions/${id}/chain`);

export const submitSession = (req: SubmitRequest) =>
  apiFetch<SubmitResponse>('/api/v1/sessions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  });

// Agents
export const listAgents = () =>
  apiFetch<Agent[]>('/api/v1/agents');

export const addAgent = (req: AddAgentRequest) =>
  apiFetch<Agent>('/api/v1/agents', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  });

export const removeAgent = (name: string) =>
  fetch(`/api/v1/agents/${encodeURIComponent(name)}`, { method: 'DELETE' });

// Queues
export const listQueues = () =>
  apiFetch<QueueInfo[]>('/api/v1/queues');

// Review
export const listReview = () =>
  apiFetch<ReviewItem[]>('/api/v1/review');
