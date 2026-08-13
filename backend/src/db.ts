import 'dotenv/config';
import { Pool } from 'pg';

export const pool = new Pool({
  connectionString: process.env.DATABASE_URL
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