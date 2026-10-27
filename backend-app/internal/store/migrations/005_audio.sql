CREATE TABLE audio_recordings (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 patient_id uuid NOT NULL REFERENCES users,
 request_key uuid NOT NULL, task text NOT NULL CHECK(task IN ('reading','free','vowel')),
 device text NOT NULL CHECK(length(device) BETWEEN 1 AND 120),
 object_key text NOT NULL UNIQUE, sha256 text NOT NULL, mime text NOT NULL,
 size_bytes integer NOT NULL CHECK(size_bytes BETWEEN 1 AND 12582912),
 consent_version text NOT NULL CHECK(consent_version='audio-v1'),
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(patient_id,request_key)
);
CREATE INDEX audio_patient_date ON audio_recordings(patient_id,created_at DESC);
CREATE TABLE audio_runs (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), recording_id uuid NOT NULL REFERENCES audio_recordings,
 status text NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','processing','ready','failed')),
 attempts integer NOT NULL DEFAULT 0, available_at timestamptz NOT NULL DEFAULT now(),
 lease_until timestamptz, lease_token text, result jsonb, error_code text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(), finished_at timestamptz
);
CREATE INDEX audio_jobs_pending ON audio_runs(available_at) WHERE status IN ('queued','processing');
CREATE TABLE audio_chunks (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), run_id uuid NOT NULL REFERENCES audio_runs,
 ordinal integer NOT NULL, start_seconds double precision NOT NULL CHECK(start_seconds>=0),
 end_seconds double precision NOT NULL CHECK(end_seconds>start_seconds),
 text text NOT NULL CHECK(length(text) BETWEEN 1 AND 4000), UNIQUE(run_id,ordinal)
);
