import { useEffect, useState } from 'react';
import { listReview, type ReviewItem } from '../api';
import { navigate } from '../router';
import './Review.css';

// The review queue is read-only in the UI -- approving/rejecting requires
// posting a HumanReviewReply to the reply_queue. That's a task insertion, not
// a plain REST call, so for now the UI shows what's pending and directs the
// operator to use `agentq review` from the CLI.
//
// TODO: add POST /api/v1/review/{task_id}/approve and /reject endpoints.

export function Review() {
  const [items, setItems] = useState<ReviewItem[]>([]);
  const [error, setError] = useState('');

  const load = () =>
    listReview()
      .then(setItems)
      .catch(e => setError(String(e)));

  useEffect(() => {
    load();
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  }, []);

  return (
    <div className="page">
      <div className="page-header">
        <h1>Review queue</h1>
        <span className="page-hint">Use <code>agentq review</code> to approve or reject.</span>
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
                {item.request.session_id.slice(0, 8)}...
              </code>
            </div>
            {item.request.context_summary && (
              <div className="review-summary">{item.request.context_summary}</div>
            )}
            <pre className="review-content">{item.request.artifact_content}</pre>
            <div className="review-task-id">Task: {item.task_id}</div>
          </div>
        ))}
      </div>
    </div>
  );
}
