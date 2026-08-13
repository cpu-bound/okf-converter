import 'dotenv/config';

import express from 'express';
import cookieParser from 'cookie-parser';
import cors from 'cors';

import authRouter from './auth';
import { checkDatabase } from './db';

const app = express();

const PORT = Number(process.env.PORT || 3000);

app.use(
  cors({
    origin: process.env.FRONTEND_URL || 'http://localhost:8080',
    credentials: true
  })
);

app.use(express.json());
app.use(cookieParser());

app.get('/api/health', (_req, res) => {
  res.json({
    status: 'ok'
  });
});

app.use('/api/auth', authRouter);

app.use(
  (
    _req,
    res
  ) => {
    res.status(404).json({
      message: 'Route not found.'
    });
  }
);

async function start(): Promise<void> {
  try {
    await checkDatabase();

    app.listen(PORT, () => {
      console.log(
        `API running on http://localhost:${PORT}`
      );
    });
  } catch (error) {
    console.error(
      'Unable to start application:',
      error
    );

    process.exit(1);
  }
}

start();