import pg from 'pg';

export interface JournalEntry {
  id?: string;
  session_id: string;
  task_id: string;
  role: 'user' | 'agent' | 'system';
  content: string;
  metadata?: Record<string, any>;
  created_at?: Date;
}

export class Journal {
  constructor(private pool: pg.Pool) {}

  async ensureTable() {
    await this.pool.query(`
      CREATE TABLE IF NOT EXISTS agentq_journal (
        id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
        session_id TEXT NOT NULL,
        task_id UUID NOT NULL,
        role TEXT NOT NULL,
        content TEXT NOT NULL,
        metadata JSONB,
        created_at TIMESTAMPTZ DEFAULT NOW()
      );
      CREATE INDEX IF NOT EXISTS idx_journal_session_id ON agentq_journal(session_id);
    `);
  }

  async append(entry: JournalEntry, client?: pg.PoolClient) {
    const db = client || this.pool;
    const res = await db.query(
      `INSERT INTO agentq_journal (session_id, task_id, role, content, metadata)
       VALUES ($1, $2, $3, $4, $5)
       RETURNING *`,
      [entry.session_id, entry.task_id, entry.role, entry.content, entry.metadata]
    );
    return res.rows[0] as JournalEntry;
  }
}
