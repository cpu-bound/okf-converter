import { Pool } from 'pg';
import { config } from './config';

export const pool = new Pool({
  connectionString: config.databaseUrl
});

export async function checkDatabase(): Promise<void> {
  const client = await pool.connect();

  try {
    await client.query('SELECT 1');
    console.log('PostgreSQL connection established');
  } finally {
    client.release();
  }
}
