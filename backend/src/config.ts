import 'dotenv/config';

function required(name: string): string {
  const value = process.env[name];

  if (!value) {
    throw new Error(`${name} is required`);
  }

  return value;
}

export const config = {
  port: Number(process.env.PORT || 3000),
  frontendUrl: process.env.FRONTEND_URL || 'http://localhost:8080',

  jwtSecret: required('JWT_SECRET'),
  databaseUrl: required('DATABASE_URL'),

  minio: {
    endpoint: process.env.MINIO_ENDPOINT || 'minio',
    port: Number(process.env.MINIO_PORT || 9000),
    useSSL: process.env.MINIO_USE_SSL === 'true',
    accessKey: required('MINIO_ACCESS_KEY'),
    secretKey: required('MINIO_SECRET_KEY'),
    bucket: process.env.MINIO_BUCKET || 'user-files',
    region: process.env.MINIO_REGION || 'us-east-1',

    // Presigned URLs are followed directly by the browser, so they must be
    // signed against an endpoint the browser can actually reach - not the
    // internal Docker network hostname used for server-to-server calls.
    publicEndpoint: process.env.MINIO_PUBLIC_ENDPOINT,
    publicPort: process.env.MINIO_PUBLIC_PORT
      ? Number(process.env.MINIO_PUBLIC_PORT)
      : undefined,
    publicUseSSL: process.env.MINIO_PUBLIC_USE_SSL === 'true'
  }
};
