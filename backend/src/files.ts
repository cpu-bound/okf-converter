import { Router, Request, Response } from 'express';
import { minioClient, presignClient, bucketName, createObjectName } from './storage';
import { pool } from './db';

const router = Router();

const MAX_FILE_SIZE = 25 * 1024 * 1024;

router.post(
  '/upload-url',
  async (req: Request, res: Response) => {
    const user = req.user!;

    const {
      filename,
      contentType,
      size
    } = req.body;

    if (
      typeof filename !== 'string' ||
      typeof contentType !== 'string' ||
      typeof size !== 'number'
    ) {
      return res.status(400).json({
        message:
          'filename, contentType and size are required.'
      });
    }

    if (size <= 0 || size > MAX_FILE_SIZE) {
      return res.status(400).json({
        message:
          'File must be between 1 byte and 25 MB.'
      });
    }

    const objectName = createObjectName(
      user.id,
      filename
    );

    const result = await pool.query(
      `
      INSERT INTO files (
        user_id,
        object_key,
        original_name,
        content_type,
        size
      )
      VALUES (
        $1,
        $2,
        $3,
        $4,
        $5
      )
      RETURNING
        id,
        original_name,
        content_type,
        size,
        status
      `,
      [
        user.id,
        objectName,
        filename,
        contentType,
        size
      ]
    );

    const uploadUrl =
      await presignClient.presignedPutObject(
        bucketName,
        objectName,
        15 * 60
      );

    return res.json({
      file: result.rows[0],
      uploadUrl
    });
  }
);

router.post(
  '/:id/confirm',
  async (req: Request, res: Response) => {
    const user = req.user!;
    const fileId = req.params.id;

    const result = await pool.query(
      `
      SELECT id, object_key, size, status
      FROM files
      WHERE id = $1
        AND user_id = $2
      `,
      [fileId, user.id]
    );

    if (result.rows.length === 0) {
      return res.status(404).json({
        message: 'File not found.'
      });
    }

    const file = result.rows[0];

    let stat;

    try {
      stat = await minioClient.statObject(
        bucketName,
        file.object_key
      );
    } catch {
      return res.status(409).json({
        message:
          'Upload was not found in storage. It may have failed or the link expired.'
      });
    }

    if (Number(stat.size) !== Number(file.size)) {
      await minioClient
        .removeObject(bucketName, file.object_key)
        .catch(() => undefined);

      await pool.query(
        `DELETE FROM files WHERE id = $1`,
        [file.id]
      );

      return res.status(409).json({
        message:
          'Uploaded file does not match the declared size.'
      });
    }

    const updated = await pool.query(
      `
      UPDATE files
      SET status = 'ready'
      WHERE id = $1
      RETURNING id, original_name, content_type, size, status
      `,
      [file.id]
    );

    return res.json({
      file: updated.rows[0]
    });
  }
);

export default router;
