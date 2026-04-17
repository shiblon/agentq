import { useEffect, useState } from 'react';
import { listSessions, submitSession, type SessionSummary } from '../api';
import { StatusBadge } from '../components/StatusBadge';
import { navigate } from '../router';
import './Sessions.css';

const STATUSES = ['', 'pending', 'in_progress', 'awaiting_review', 'completed', 'failed'];

function age(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime();
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

function truncate(s: string, n: number): string {
  const flat = s.replace(/\s+/g, ' ').trim();
  return flat.length > n ? flat.slice(0, n) + '...' : flat;
}

export function Sessions() {
  const [sessions, setSessions] = useState<SessionSummary[]>([]);
  const [statusFilter, setStatusFilter] = useState('');
  const [error, setError] = useState('');
  const [showSubmit, setShowSubmit] = useState(false);
  const [prompt, setPrompt] = useState('');
  const [repo, setRepo] = useState('');
  const [submitting, setSubmitting] = useState(false);

  const load = () =>
    listSessions(statusFilter || undefined, 100)
      .then(setSessions)
      .catch(e => setError(String(e)));

  useEffect(() => {
    load();
    const t = setInterval(load, 4000);
    return () => clearInterval(t);
  }, [statusFilter]);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!prompt.trim()) return;
    setSubmitting(true);
    try {
      const req: Parameters<typeof submitSession>[0] = { prompt: prompt.trim() };
      if (repo.trim()) req.repo = repo.trim();
      const res = await submitSession(req);
      setPrompt('');
      setRepo('');
      setShowSubmit(false);
      navigate(`/sessions/${res.session_id}`);
    } catch (e) {
      setError(String(e));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="page">
      <div className="page-header">
        <h1>Sessions</h1>
        <button className="btn-primary" onClick={() => setShowSubmit(v => !v)}>
          {showSubmit ? 'Cancel' : '+ New session'}
        </button>
      </div>

      {showSubmit && (
        <form className="submit-form" onSubmit={handleSubmit}>
          <textarea
            className="submit-textarea"
            placeholder="Enter a prompt..."
            value={prompt}
            onChange={e => setPrompt(e.target.value)}
            rows={4}
            autoFocus
          />
          <input
            className="submit-input"
            type="text"
            placeholder="Workspace repo (optional, e.g. github.com/shiblon/agentq)"
            value={repo}
            onChange={e => setRepo(e.target.value)}
          />
          <button className="btn-primary" type="submit" disabled={submitting}>
            {submitting ? 'Submitting...' : 'Submit'}
          </button>
        </form>
      )}

      <div className="filter-tabs">
        {STATUSES.map(s => (
          <button
            key={s}
            className={`filter-tab${statusFilter === s ? ' active' : ''}`}
            onClick={() => setStatusFilter(s)}
          >
            {s || 'all'}
          </button>
        ))}
      </div>

      {error && <div className="error-msg">{error}</div>}

      <table className="data-table">
        <thead>
          <tr>
            <th>Status</th>
            <th>Prompt</th>
            <th>Artifacts</th>
            <th>Updated</th>
          </tr>
        </thead>
        <tbody>
          {sessions.map(s => (
            <tr key={s.id} className="clickable" onClick={() => navigate(`/sessions/${s.id}`)}>
              <td><StatusBadge status={s.status} /></td>
              <td className="col-prompt">{truncate(s.prompt, 80)}</td>
              <td className="col-num">{s.artifact_count}</td>
              <td className="col-age">{age(s.updated_at)}</td>
            </tr>
          ))}
          {sessions.length === 0 && (
            <tr><td colSpan={4} className="empty-msg">No sessions.</td></tr>
          )}
        </tbody>
      </table>
    </div>
  );
}
