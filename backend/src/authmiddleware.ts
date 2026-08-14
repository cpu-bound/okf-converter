import { Request, Response, NextFunction } from 'express';
import { getUserFromToken, AuthUser } from './auth';

declare global {
  namespace Express {
    interface Request {
      user?: AuthUser;
    }
  }
}

export async function requireAuth(
  req: Request,
  res: Response,
  next: NextFunction
): Promise<void> {
  const user = await getUserFromToken(req);

  if (!user) {
    res.status(401).json({
      message: 'Not authenticated.'
    });

    return;
  }

  req.user = user;

  next();
}
