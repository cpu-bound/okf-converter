import * as Minio from 'minio';
import crypto from 'crypto';
import { config } from './config';

export const bucketName = config.minio.bucket;

export const minioClient = new Minio.Client({
  endPoint: config.minio.endpoint,
  port: config.minio.port,
  useSSL: config.minio.useSSL,
  accessKey: config.minio.accessKey,
  secretKey: config.minio.secretKey,
  region: config.minio.region
});

export const presignClient = new Minio.Client({
  endPoint: config.minio.publicEndpoint || config.minio.endpoint,
  port: config.minio.publicPort || config.minio.port,
  useSSL: config.minio.publicUseSSL || config.minio.useSSL,
  accessKey: config.minio.accessKey,
  secretKey: config.minio.secretKey,
  region: config.minio.region
});

export async function initializeStorage(): Promise<void> {
  const exists =
    await minioClient.bucketExists(bucketName);

  if (!exists) {
    await minioClient.makeBucket(
      bucketName,
      config.minio.region
    );

    console.log(
      `Created MinIO bucket: ${bucketName}`
    );
  } else {
    console.log(
      `MinIO bucket exists: ${bucketName}`
    );
  }
}

export function createObjectName(
  userId: string,
  originalName: string
): string {
  const extension =
    originalName
      .split('.')
      .pop()
      ?.toLowerCase() || '';

  const id =
    crypto.randomUUID();

  if (!extension) {
    return `${userId}/${id}`;
  }

  return `${userId}/${id}.${extension}`;
}
