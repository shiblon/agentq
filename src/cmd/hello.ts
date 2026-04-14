import { EntroQPG, EntroQWorker, Task } from 'entroq';
import pg from 'pg';
import { Journal } from '../core/journal.js';

const connectionString = process.env.DATABASE_URL || 'postgres://entroq:entroq@localhost:5432/entroq';

async function main() {
  const pool = new pg.Pool({ connectionString });
  const journal = new Journal(pool);
  
  // Ensure the journal table exists
  await journal.ensureTable();

  // EntroQPG expects a config object.
  const client = new EntroQPG({ connectionString });
  await client.connect();

  console.log('🚀 AgentQ Hello Worker starting...');

  const worker = new EntroQWorker(client);

  // The TS worker uses a simpler (task, stop) => mods pattern for now.
  const handler = async (task: Task, stop: () => Promise<Task>) => {
    console.log(`📥 Claimed task: ${task.id}`);
    
    const payload = task.value as { prompt: string; session_id: string };
    const response = `Hello! You said: "${payload.prompt}". I am a helpful agent.`;

    console.log(`📝 Journaling response for session: ${payload.session_id}`);
    
    // For now, journaling is a separate call until we add Tx support to the TS client.
    await journal.append({
      session_id: payload.session_id,
      task_id: task.id,
      role: 'agent',
      content: response,
      metadata: { response_type: 'hello_world' }
    });

    // Halting renewal and getting the stable task before finishing.
    const currentTask = await stop();

    // Returning the delete modification.
    return {
        deletes: [{ id: currentTask.id, version: currentTask.version, queue: currentTask.queue }]
    };
  };

  // Seed a task if the queue is empty (for demo purposes)
  // (Using tryClaim with 0 duration to check)
  const initialTask = await client.tryClaim('agentq.hello', 0);
  if (!initialTask) {
    console.log('🌱 Seeding initial task...');
    await client.modify({
      inserts: [{
        queue: 'agentq.hello',
        atMs: '0',
        value: { prompt: 'What is the meaning of life?', session_id: 'session-42' }
      }]
    });
  } else {
    // If we claimed it during the check, we should probably just let it go or put it back.
    // For this demo, we'll just delete it and re-insert it to be clean.
    await client.modify({ deletes: [{ id: initialTask.id, version: initialTask.version, queue: initialTask.queue }] });
    await client.modify({
        inserts: [{
          queue: 'agentq.hello',
          atMs: '0',
          value: { prompt: 'What is the meaning of life?', session_id: 'session-42' }
        }]
      });
  }

  await worker.run(['agentq.hello'], handler);
}

main().catch(console.error);
