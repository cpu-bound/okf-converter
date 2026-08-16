CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    name VARCHAR(100) NOT NULL,

    email VARCHAR(255) NOT NULL UNIQUE,

    password_hash TEXT NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS users_email_idx
    ON users(email);

CREATE TABLE IF NOT EXISTS files (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    user_id UUID NOT NULL
        REFERENCES users(id)
        ON DELETE CASCADE,

    object_key TEXT NOT NULL UNIQUE,

    original_name TEXT NOT NULL,

    content_type TEXT NOT NULL,

    size BIGINT NOT NULL,

    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'ready', 'converting', 'converted', 'failed')),

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS files_user_id_idx
    ON files(user_id);

-- Un registro por archivo del bundle generado (index.md, log.md y cada
-- documento de concepto), en el orden en que se leen.
CREATE TABLE IF NOT EXISTS file_outputs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    file_id UUID NOT NULL
        REFERENCES files(id)
        ON DELETE CASCADE,

    object_key TEXT NOT NULL UNIQUE,

    -- Nombre dentro del bundle, p. ej. 'index.md' o '02-metodologia.md'.
    name TEXT NOT NULL,

    -- Orden de lectura dentro del bundle, empezando en 0.
    position INT NOT NULL,

    size BIGINT NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS file_outputs_file_id_idx
    ON file_outputs(file_id);

CREATE TABLE IF NOT EXISTS conversion_jobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    file_id UUID NOT NULL
        REFERENCES files(id)
        ON DELETE CASCADE,

    retry_of UUID
        REFERENCES conversion_jobs(id),

    status TEXT NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'converting', 'converted', 'failed')),

    error TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    finished_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS conversion_jobs_file_id_idx
    ON conversion_jobs(file_id);