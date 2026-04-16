import { useEffect, useState } from 'react';
import { listQueues, type QueueInfo } from '../api';
import './Queues.css';

export function Queues() {
  const [queues, setQueues] = useState<QueueInfo[]>([]);
  const [error, setError] = useState('');

  const load = () =>
    listQueues()
      .then(q => setQueues([...q].sort((a, b) => a.name.localeCompare(b.name))))
      .catch(e => setError(String(e)));

  useEffect(() => {
    load();
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  }, []);

  return (
    <div className="page">
      <div className="page-header">
        <h1>Queues</h1>
      </div>

      {error && <div className="error-msg">{error}</div>}

      <table className="data-table">
        <thead>
          <tr>
            <th>Queue</th>
            <th>Pending</th>
            <th>Total</th>
            <th>Utilization</th>
          </tr>
        </thead>
        <tbody>
          {queues.map(q => {
            const pct = q.total > 0 ? Math.round((q.pending / q.total) * 100) : 0;
            return (
              <tr key={q.name}>
                <td><code>{q.name}</code></td>
                <td className="col-num">{q.pending}</td>
                <td className="col-num">{q.total}</td>
                <td className="col-bar">
                  <div className="util-bar">
                    <div className="util-fill" style={{ width: `${pct}%` }} />
                  </div>
                  <span className="util-pct">{pct}%</span>
                </td>
              </tr>
            );
          })}
          {queues.length === 0 && !error && (
            <tr><td colSpan={4} className="empty-msg">No queues.</td></tr>
          )}
        </tbody>
      </table>
    </div>
  );
}
