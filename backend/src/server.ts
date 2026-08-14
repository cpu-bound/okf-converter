import express, { NextFunction, Request, Response } from 'express';
import cookieParser from 'cookie-parser';
import cors from 'cors';

import authRouter from './auth';
import { checkDatabase } from './db';

import filesRouter from './files';
import { requireAuth } from './authmiddleware';
import { initializeStorage } from './storage';
import { config } from './config';

const app = express();

app.use(
  cors({
    origin: config.frontendUrl,
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
  '/api/files',
  requireAuth,
  filesRouter
);

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

app.use(
  (
    err: unknown,
    _req: Request,
    res: Response,
    // Express only recognizes this as an error handler when it takes
    // four arguments - _next must stay even though it's unused.
    _next: NextFunction
  ) => {
    console.error('Unhandled error:', err);

    res.status(500).json({
      message: 'Something went wrong.'
    });
  }
);

async function start(): Promise<void> {
  try {
    await checkDatabase();

    await initializeStorage();

    app.listen(config.port, () => {
      console.log(
        `API running on http://localhost:${config.port}`
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
