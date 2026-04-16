import { useEffect, useState } from 'react';
import { listAgents, addAgent, removeAgent, type Agent } from '../api';
import './Agents.css';

export function Agents() {
  const [agents, setAgents] = useState<Agent[]>([]);
  const [error, setError] = useState('');
  const [showAdd, setShowAdd] = useState(false);
  const [form, setForm] = useState({ name: '', queue: '', description: '', cmd: '', approval_suffix: '' });
  const [saving, setSaving] = useState(false);

  const load = () =>
    listAgents()
      .then(setAgents)
      .catch(e => setError(String(e)));

  useEffect(() => { load(); }, []);

  const handleAdd = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!form.name || !form.queue || !form.description) return;
    setSaving(true);
    try {
      await addAgent({
        name: form.name,
        queue: form.queue,
        description: form.description,
        cmd: form.cmd || undefined,
        approval_suffix: form.approval_suffix || undefined,
      });
      setForm({ name: '', queue: '', description: '', cmd: '', approval_suffix: '' });
      setShowAdd(false);
      load();
    } catch (e) {
      setError(String(e));
    } finally {
      setSaving(false);
    }
  };

  const handleRemove = async (name: string) => {
    if (!confirm(`Remove agent "${name}"?`)) return;
    try {
      await removeAgent(name);
      load();
    } catch (e) {
      setError(String(e));
    }
  };

  return (
    <div className="page">
      <div className="page-header">
        <h1>Agents</h1>
        <button className="btn-primary" onClick={() => setShowAdd(v => !v)}>
          {showAdd ? 'Cancel' : '+ Add agent'}
        </button>
      </div>

      {showAdd && (
        <form className="add-form" onSubmit={handleAdd}>
          <div className="form-row">
            <label>Name<input value={form.name} onChange={e => setForm(f => ({ ...f, name: e.target.value }))} required /></label>
            <label>Queue<input value={form.queue} onChange={e => setForm(f => ({ ...f, queue: e.target.value }))} required /></label>
          </div>
          <label>Description<input value={form.description} onChange={e => setForm(f => ({ ...f, description: e.target.value }))} required /></label>
          <label>Command (optional)<input value={form.cmd} onChange={e => setForm(f => ({ ...f, cmd: e.target.value }))} placeholder="e.g. claude --print" /></label>
          <label>Approval suffix (optional)<input value={form.approval_suffix} onChange={e => setForm(f => ({ ...f, approval_suffix: e.target.value }))} placeholder="e.g. --dangerously-skip-permissions" /></label>
          <button className="btn-primary" type="submit" disabled={saving}>
            {saving ? 'Saving...' : 'Save'}
          </button>
        </form>
      )}

      {error && <div className="error-msg">{error}</div>}

      <table className="data-table">
        <thead>
          <tr>
            <th>Name</th>
            <th>Queue</th>
            <th>Description</th>
            <th>Command</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {agents.map(a => (
            <tr key={a.name}>
              <td><strong>{a.name}</strong></td>
              <td><code>{a.queue}</code></td>
              <td>{a.description}</td>
              <td><code>{a.cmd || '-'}</code></td>
              <td>
                <button className="btn-danger-sm" onClick={() => handleRemove(a.name)}>Remove</button>
              </td>
            </tr>
          ))}
          {agents.length === 0 && (
            <tr><td colSpan={5} className="empty-msg">No agents configured.</td></tr>
          )}
        </tbody>
      </table>
    </div>
  );
}
