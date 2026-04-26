import { useEffect, useState } from 'react';
import { listReview, approveReview, rejectReview, type ReviewItem } from '../api';
import { navigate } from '../router';
import './Review.css';

export function Review() {
  const [items, setItems] = useState<ReviewItem[]>([]);
  const [error, setError] = useState('');
  const [inputs, setInputs] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState<Record<string, boolean>>({});

  const load = () =>
    listReview()
      .then(setItems)
      .catch(e => setError(String(e)));

  useEffect(() => {
    load();
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  }, []);

  const act = async (taskId: string, action: 'approve' | 'reject') => {
    setBusy(b => ({ ...b, [taskId]: true }));
    try {
      const input = inputs[taskId] ?? '';
      if (action === 'approve') await approveReview(taskId, input);
      else await rejectReview(taskId, input);
      setItems(prev => prev.filter(i => i.task_id !== taskId));
      setInputs(({ [taskId]: _, ...rest }) => rest);
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(b => ({ ...b, [taskId]: false }));
    }
  };

  return (
    <div className="page">
      <div className="page-header">
        <h1>Review queue</h1>
      </div>

      {error && <div className="error-msg">{error}</div>}

      {items.length === 0 && !error && (
        <div className="empty-msg">No pending reviews.</div>
      )}

      <div className="review-list">
        {items.map(item => (
          <div key={item.task_id} className="review-card">
            <div className="review-header">
              <span className="review-agent">{item.request.agent_name}</span>
              <span className="review-at">{new Date(item.at).toLocaleString()}</span>
              <code
                className="review-session-link"
                onClick={() => navigate(`/sessions/${item.request.session_id}`)}
                title="View session"
              >
                {item.request.session_id.slice(0, 8)}…
              </code>
            </div>
            {item.request.context_summary && (
              <div className="review-summary">{item.request.context_summary}</div>
            )}
            <pre className="review-content">{item.request.artifact_content}</pre>
            <div className="review-actions">
              <textarea
                className="review-input"
                placeholder="Optional comment…"
                value={inputs[item.task_id] ?? ''}
                onChange={e => setInputs(prev => ({ ...prev, [item.task_id]: e.target.value }))}
                rows={2}
              />
              <div className="review-buttons">
                <button
                  className="btn-approve"
                  disabled={busy[item.task_id]}
                  onClick={() => act(item.task_id, 'approve')}
                >
                  Approve
                </button>
                <button
                  className="btn-reject"
                  disabled={busy[item.task_id]}
                  onClick={() => act(item.task_id, 'reject')}
                >
                  Reject
                </button>
              </div>
            </div>
            <div className="review-task-id">Task: {item.task_id}</div>
          </div>
        ))}
      </div>
    </div>
  );
}
