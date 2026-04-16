import { useEffect, useState } from 'react';
import { getSessionChain, type Session, type Artifact } from '../api';
import { StatusBadge } from '../components/StatusBadge';
import { navigate } from '../router';
import './SessionDetail.css';

interface Props {
  id: string;
}

function fmtTime(iso: string): string {
  return new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
}

function ArtifactCard({ a }: { a: Artifact }) {
  const [expanded, setExpanded] = useState(false);
  const isLong = a.content.length > 500;
  const display = (!isLong || expanded) ? a.content : a.content.slice(0, 500) + '...';

  return (
    <div className={`artifact-card${a.origin_session_id ? ' inherited' : ''}`}>
      <div className="artifact-header">
        <span className="artifact-agent">{a.agent_name}</span>
        {a.origin_session_id && (
          <span
            className="artifact-origin"
            title={a.origin_session_id}
            onClick={() => navigate(`/sessions/${a.origin_session_id}`)}
          >
            inherited
          </span>
        )}
        <span className="artifact-type">{a.content_type || 'text'}</span>
        <span className="artifact-time">{fmtTime(a.created_at)}</span>
      </div>
      <pre className="artifact-content">{display}</pre>
      {isLong && (
        <button className="artifact-toggle" onClick={() => setExpanded(v => !v)}>
          {expanded ? 'collapse' : 'expand'}
        </button>
      )}
    </div>
  );
}

export function SessionDetail({ id }: Props) {
  const [chain, setChain] = useState<Session[]>([]);
  const [error, setError] = useState('');
  const [focused, setFocused] = useState<string>(id);

  const load = () =>
    getSessionChain(id)
      .then(c => { setChain(c); })
      .catch(e => setError(String(e)));

  useEffect(() => {
    setFocused(id);
    load();
    const t = setInterval(load, 4000);
    return () => clearInterval(t);
  }, [id]);

  if (error) return <div className="page"><div className="error-msg">{error}</div></div>;

  const session = chain.find(s => s.id === focused) ?? chain[chain.length - 1];

  return (
    <div className="page">
      <div className="page-header">
        <button className="btn-back" onClick={() => navigate('/sessions')}>&larr; Sessions</button>
        {session && <StatusBadge status={session.status} />}
      </div>

      {chain.length > 1 && (
        <div className="chain-nav">
          {chain.map((s, i) => (
            <button
              key={s.id}
              className={`chain-item${s.id === focused ? ' active' : ''}`}
              onClick={() => setFocused(s.id)}
              title={s.prompt}
            >
              {i + 1}: {s.prompt.slice(0, 30)}{s.prompt.length > 30 ? '...' : ''}
            </button>
          ))}
        </div>
      )}

      {session && (
        <>
          <div className="session-prompt">{session.prompt}</div>
          <div className="session-meta">
            <span>ID: <code>{session.id}</code></span>
            {session.parent_session_id && (
              <span>
                Parent: <code
                  className="link"
                  onClick={() => navigate(`/sessions/${session.parent_session_id}`)}
                >{session.parent_session_id.slice(0, 8)}...</code>
              </span>
            )}
          </div>
          <div className="artifacts-list">
            {session.artifacts?.length === 0 && <div className="empty-msg">No artifacts yet.</div>}
            {session.artifacts?.map((a, i) => (
              <ArtifactCard key={i} a={a} />
            ))}
          </div>
        </>
      )}
    </div>
  );
}
