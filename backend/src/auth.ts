import { Router, Request, Response } from 'express';
import argon2 from 'argon2';
import jwt from 'jsonwebtoken';
import { pool } from './db';
import { config } from './config';

const router = Router();

export interface AuthUser {
  id: string;
  name: string;
  email: string;
}

function createToken(user: AuthUser): string {
  return jwt.sign(
    {
      sub: user.id,
      email: user.email
    },
    config.jwtSecret,
    {
      expiresIn: '1h'
    }
  );
}

function setAuthCookie(res: Response, token: string): void {
  res.cookie('auth_token', token, {
    httpOnly: true,
    secure: process.env.NODE_ENV === 'production',
    sameSite: 'lax',
    maxAge: 60 * 60 * 1000,
    path: '/'
  });
}

function clearAuthCookie(res: Response): void {
  res.clearCookie('auth_token', {
    httpOnly: true,
    secure: process.env.NODE_ENV === 'production',
    sameSite: 'lax',
    path: '/'
  });
}

function getToken(req: Request): string | undefined {
  return req.cookies?.auth_token;
}

export async function getUserFromToken(
  req: Request
): Promise<AuthUser | null> {
  const token = getToken(req);

  if (!token) {
    return null;
  }

  try {
    const payload =
      jwt.verify(
        token,
        config.jwtSecret
      ) as jwt.JwtPayload;

    if (!payload.sub) {
      return null;
    }

    const result =
      await pool.query(
        `
        SELECT id, name, email
        FROM users
        WHERE id = $1
        `,
        [payload.sub]
      );

    if (result.rows.length === 0) {
      return null;
    }

    return result.rows[0];
  } catch {
    return null;
  }
}

/**
 * POST /api/auth/register
 */
router.post(
  '/register',
  async (req: Request, res: Response) => {
    const { name, email, password } = req.body;

    if (
      typeof name !== 'string' ||
      typeof email !== 'string' ||
      typeof password !== 'string'
    ) {
      return res.status(400).json({
        message: 'Name, email and password are required.'
      });
    }

    const normalizedEmail = email.trim().toLowerCase();

    if (name.trim().length < 2) {
      return res.status(400).json({
        message: 'Name must contain at least 2 characters.'
      });
    }

    if (password.length < 8) {
      return res.status(400).json({
        message: 'Password must contain at least 8 characters.'
      });
    }

    const existing = await pool.query(
      `
      SELECT id
      FROM users
      WHERE email = $1
      `,
      [normalizedEmail]
    );

    if (existing.rows.length > 0) {
      return res.status(409).json({
        message: 'An account with this email already exists.'
      });
    }

    const passwordHash = await argon2.hash(password);

    const result = await pool.query(
      `
      INSERT INTO users (
        name,
        email,
        password_hash
      )
      VALUES ($1, $2, $3)
      RETURNING id, name, email
      `,
      [
        name.trim(),
        normalizedEmail,
        passwordHash
      ]
    );

    const user = result.rows[0];

    const token = createToken(user);

    setAuthCookie(res, token);

    return res.status(201).json({
      user
    });
  }
);

/**
 * POST /api/auth/login
 */
router.post(
  '/login',
  async (req: Request, res: Response) => {
    const { email, password } = req.body;

    if (
      typeof email !== 'string' ||
      typeof password !== 'string'
    ) {
      return res.status(400).json({
        message: 'Email and password are required.'
      });
    }

    const normalizedEmail = email.trim().toLowerCase();

    const result = await pool.query(
      `
      SELECT id, name, email, password_hash
      FROM users
      WHERE email = $1
      `,
      [normalizedEmail]
    );

    if (result.rows.length === 0) {
      return res.status(401).json({
        message: 'Invalid email or password.'
      });
    }

    const dbUser = result.rows[0];

    const passwordValid = await argon2.verify(
      dbUser.password_hash,
      password
    );

    if (!passwordValid) {
      return res.status(401).json({
        message: 'Invalid email or password.'
      });
    }

    const user: AuthUser = {
      id: dbUser.id,
      name: dbUser.name,
      email: dbUser.email
    };

    const token = createToken(user);

    setAuthCookie(res, token);

    return res.json({
      user
    });
  }
);

/**
 * GET /api/auth/me
 */
router.get(
  '/me',
  async (req: Request, res: Response) => {
    const user = await getUserFromToken(req);

    if (!user) {
      return res.status(401).json({
        message: 'Not authenticated.'
      });
    }

    return res.json({
      user
    });
  }
);

/**
 * POST /api/auth/logout
 */
router.post(
  '/logout',
  (_req: Request, res: Response) => {
    clearAuthCookie(res);

    return res.json({
      message: 'Logged out successfully.'
    });
  }
);

export default router;
