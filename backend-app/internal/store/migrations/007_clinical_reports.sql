CREATE TABLE clinical_context_versions (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), patient_id uuid NOT NULL REFERENCES users,
 version integer NOT NULL CHECK(version>0), author_id uuid NOT NULL REFERENCES users,
 context jsonb NOT NULL CHECK(jsonb_typeof(context)='object'),
 request_key uuid NOT NULL, request_hash text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(patient_id,version), UNIQUE(patient_id,author_id,request_key)
);
CREATE INDEX clinical_context_history ON clinical_context_versions(patient_id,created_at DESC,id DESC);
CREATE TABLE clinical_audio_reports (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), patient_id uuid NOT NULL REFERENCES users,
 recording_id uuid NOT NULL REFERENCES audio_recordings, run_id uuid NOT NULL REFERENCES audio_runs,
 context_id uuid REFERENCES clinical_context_versions, author_id uuid NOT NULL REFERENCES users,
 source_snapshot jsonb NOT NULL, description text NOT NULL,
 request_key uuid NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(recording_id,author_id,request_key)
);
CREATE INDEX clinical_audio_report_history ON clinical_audio_reports(recording_id,created_at DESC,id DESC);
CREATE TABLE voicefm_jobs (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), report_id uuid NOT NULL UNIQUE REFERENCES clinical_audio_reports,
 status text NOT NULL CHECK(status IN ('queued','processing','ready','model_unavailable','failed')),
 attempts integer NOT NULL DEFAULT 0, available_at timestamptz NOT NULL DEFAULT now(),
 lease_token text, lease_until timestamptz, reason_code text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(), finished_at timestamptz
);
CREATE INDEX voicefm_pending_jobs ON voicefm_jobs(available_at) WHERE status IN ('queued','processing');
CREATE TABLE voicefm_results (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), report_id uuid NOT NULL UNIQUE REFERENCES clinical_audio_reports,
 result jsonb NOT NULL CHECK(jsonb_typeof(result)='object'), created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE clinical_report_conclusions (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), report_id uuid NOT NULL REFERENCES clinical_audio_reports,
 author_id uuid NOT NULL REFERENCES users, text text NOT NULL CHECK(length(text) BETWEEN 1 AND 10000),
 request_key uuid NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(report_id,author_id,request_key)
);
CREATE INDEX clinical_report_conclusion_history ON clinical_report_conclusions(report_id,created_at,id);
COMMENT ON TABLE clinical_context_versions IS 'Immutable doctor-authored context and sourced external scale totals; no patient questionnaire or generated diagnosis.';
COMMENT ON TABLE clinical_audio_reports IS 'Immutable source-bound descriptive report; model results and attributable clinician conclusions are separate append-only records.';
COMMENT ON TABLE voicefm_results IS 'Immutable model output with explicit mode/provenance. Authorized mock fixtures are labeled synthetic, clinical_use=false and input_used_for_prediction=false; real output requires verified upstream weights/probes.';
