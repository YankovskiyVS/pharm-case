-- Extend existing records without rewriting already applied migrations.
ALTER TABLE users ADD COLUMN auth_version bigint NOT NULL DEFAULT 1;
ALTER TABLE sessions ADD COLUMN id uuid NOT NULL DEFAULT gen_random_uuid() UNIQUE;
ALTER TABLE sessions ADD COLUMN created_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE sessions ADD COLUMN auth_version bigint NOT NULL DEFAULT 1;
ALTER TABLE sessions ADD COLUMN auth_role text;
UPDATE sessions s SET auth_version=u.auth_version,auth_role=u.role FROM users u WHERE u.id=s.user_id;
ALTER TABLE sessions ALTER COLUMN auth_role SET NOT NULL;
CREATE FUNCTION bind_session_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 SELECT auth_version,role INTO NEW.auth_version,NEW.auth_role FROM users WHERE id=NEW.user_id;
 RETURN NEW;
END $$;
CREATE TRIGGER session_identity_before_insert BEFORE INSERT ON sessions FOR EACH ROW EXECUTE FUNCTION bind_session_identity();
CREATE FUNCTION invalidate_changed_credentials() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.password_hash IS DISTINCT FROM OLD.password_hash OR NEW.totp_secret IS DISTINCT FROM OLD.totp_secret OR NEW.role IS DISTINCT FROM OLD.role OR NEW.active IS DISTINCT FROM OLD.active THEN
  NEW.auth_version=OLD.auth_version+1;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER user_credential_version BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION invalidate_changed_credentials();
-- Even a legacy unconfigured doctor can be disabled immediately by the DB operator.
ALTER TABLE users DROP CONSTRAINT doctor_totp_configured;
ALTER TABLE users ADD CONSTRAINT doctor_totp_configured CHECK (NOT active OR role!='doctor' OR totp_secret ~ '^[A-Z2-7]{26,128}$') NOT VALID;

CREATE TABLE password_resets (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), user_id uuid NOT NULL REFERENCES users,
 token_hash text NOT NULL UNIQUE, expires_at timestamptz NOT NULL, used_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE email_jobs DROP CONSTRAINT email_jobs_source_type_check;
ALTER TABLE email_jobs ADD CONSTRAINT email_jobs_source_type_check CHECK(source_type IN ('notification','invitation','password_reset','operational'));
ALTER TABLE email_jobs ADD COLUMN password_reset_id uuid REFERENCES password_resets;
ALTER TABLE email_jobs ADD COLUMN alert_after timestamptz;
ALTER TABLE email_jobs ADD COLUMN failure_notified_at timestamptz;
CREATE TABLE notification_reads (
 notification_id uuid NOT NULL REFERENCES notifications(id), user_id uuid NOT NULL REFERENCES users,
 read_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(notification_id,user_id)
);
CREATE TABLE operational_exceptions (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), kind text NOT NULL,
 aggregate_id uuid NOT NULL, status text NOT NULL DEFAULT 'open' CHECK(status IN ('open','resolved')),
 safe_code text NOT NULL, attempts integer NOT NULL DEFAULT 1,
 first_seen_at timestamptz NOT NULL DEFAULT now(), last_seen_at timestamptz NOT NULL DEFAULT now(),
 resolved_at timestamptz, UNIQUE(kind,aggregate_id)
);
CREATE INDEX operational_open ON operational_exceptions(first_seen_at) WHERE status='open';

ALTER TABLE care_links ADD COLUMN created_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE assignments DROP CONSTRAINT assignments_patient_id_kind_key;
ALTER TABLE assignments ADD COLUMN status text NOT NULL DEFAULT 'active' CHECK(status IN ('active','paused','cancelled'));
ALTER TABLE assignments ADD COLUMN interval_days integer NOT NULL DEFAULT 1 CHECK(interval_days BETWEEN 1 AND 365);
ALTER TABLE assignments ADD COLUMN created_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE assignments ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE assignments ADD COLUMN request_key uuid;
ALTER TABLE assignments ADD COLUMN request_hash text;
CREATE UNIQUE INDEX assignment_request ON assignments(created_by,request_key) WHERE request_key IS NOT NULL;
CREATE TABLE assignment_history (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), assignment_id uuid NOT NULL REFERENCES assignments,
 patient_id uuid NOT NULL REFERENCES users, actor_id uuid NOT NULL REFERENCES users,
 operation text NOT NULL, snapshot jsonb NOT NULL,
 request_key uuid, request_hash text, created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(actor_id,request_key)
);
INSERT INTO assignment_history(assignment_id,patient_id,actor_id,operation,snapshot)
 SELECT id,patient_id,created_by,'migrated',to_jsonb(a) FROM assignments a;
ALTER TABLE reviews ADD COLUMN request_key uuid;
ALTER TABLE reviews ADD COLUMN request_hash text;
CREATE UNIQUE INDEX review_request ON reviews(doctor_id,request_key) WHERE request_key IS NOT NULL;
CREATE TABLE weekly_review_reminders (
 doctor_id uuid NOT NULL REFERENCES users, patient_id uuid NOT NULL REFERENCES users,
 due_date date NOT NULL, event_id uuid NOT NULL DEFAULT gen_random_uuid(),
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(doctor_id,patient_id,due_date)
);
CREATE INDEX diary_history_cursor ON diary_entries(patient_id,created_at DESC,id DESC);
CREATE INDEX review_history_cursor ON reviews(patient_id,created_at DESC,id DESC);
CREATE TABLE message_reads (
 message_id uuid NOT NULL REFERENCES messages, user_id uuid NOT NULL REFERENCES users,
 read_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(message_id,user_id)
);
CREATE INDEX audio_run_recording_history ON audio_runs(recording_id,created_at DESC,id DESC);
