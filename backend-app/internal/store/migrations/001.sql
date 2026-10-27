CREATE TABLE users (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), email text NOT NULL UNIQUE,
 name text NOT NULL, role text NOT NULL CHECK(role IN ('doctor','patient')),
 password_hash text NOT NULL, totp_secret text NOT NULL DEFAULT '',
 active boolean NOT NULL DEFAULT true, timezone text NOT NULL DEFAULT 'Europe/Moscow',
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE sessions (
 token_hash text PRIMARY KEY, user_id uuid NOT NULL REFERENCES users,
 csrf text NOT NULL, expires_at timestamptz NOT NULL
);
CREATE TABLE consents (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), user_id uuid NOT NULL REFERENCES users, version text NOT NULL, accepted_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE care_links (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), doctor_id uuid NOT NULL REFERENCES users,
 patient_id uuid NOT NULL REFERENCES users, active boolean NOT NULL DEFAULT true,
 UNIQUE(doctor_id,patient_id)
);
CREATE TABLE invitations (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), token_hash text UNIQUE NOT NULL,
 doctor_id uuid NOT NULL REFERENCES users, email text NOT NULL, name text NOT NULL,
 expires_at timestamptz NOT NULL, accepted_at timestamptz
);
CREATE TABLE assignments (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), patient_id uuid NOT NULL REFERENCES users,
 kind text NOT NULL CHECK(kind IN ('ema','csd')), start_date date NOT NULL,
 end_date date NOT NULL CHECK(end_date>=start_date), due_time time NOT NULL,
 created_by uuid NOT NULL REFERENCES users, UNIQUE(patient_id,kind)
);
CREATE TABLE diary_entries (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), patient_id uuid NOT NULL REFERENCES users,
 kind text NOT NULL CHECK(kind IN ('ema','csd')), local_date date NOT NULL,
 timezone text NOT NULL, version integer NOT NULL DEFAULT 1 CHECK(version=1),
 form_id uuid NOT NULL, answers jsonb NOT NULL, scores jsonb NOT NULL,
 scoring_version text NOT NULL DEFAULT 'excel-v1',
 supersedes_id uuid UNIQUE REFERENCES diary_entries,
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(patient_id,form_id)
);
CREATE INDEX diary_patient_date ON diary_entries(patient_id,local_date DESC);
CREATE VIEW current_entries AS SELECT e.* FROM diary_entries e WHERE NOT EXISTS(SELECT 1 FROM diary_entries n WHERE n.supersedes_id=e.id);
CREATE TABLE risk_signals (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), patient_id uuid NOT NULL REFERENCES users,
 form_id uuid NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(patient_id,form_id)
);
CREATE TABLE alerts (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), patient_id uuid NOT NULL REFERENCES users,
 signal_id uuid NOT NULL UNIQUE REFERENCES risk_signals, type text NOT NULL DEFAULT 'SUICIDE',
 status text NOT NULL DEFAULT 'new' CHECK(status IN ('new','ack','resolved')),
 acknowledged_by uuid REFERENCES users, acknowledged_at timestamptz, resolved_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX alerts_open ON alerts(created_at) WHERE status!='resolved';
CREATE TABLE alert_actions (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), alert_id uuid NOT NULL REFERENCES alerts, actor_id uuid NOT NULL REFERENCES users, action text NOT NULL, note text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE reviews (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), doctor_id uuid NOT NULL REFERENCES users, patient_id uuid NOT NULL REFERENCES users, action text NOT NULL CHECK(action IN ('reviewed','contact')), note text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE messages (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), care_link_id uuid NOT NULL REFERENCES care_links,
 sender_id uuid NOT NULL REFERENCES users, body text NOT NULL CHECK(length(body) BETWEEN 1 AND 4000),
 request_key uuid NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(sender_id,request_key)
);
CREATE INDEX messages_conversation ON messages(care_link_id,created_at);
CREATE TABLE outbox_events (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), type text NOT NULL, version integer NOT NULL DEFAULT 1,
 patient_id uuid NOT NULL REFERENCES users, aggregate_id uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE event_deliveries (
 event_id uuid NOT NULL REFERENCES outbox_events, consumer text NOT NULL,
 completed_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(event_id,consumer)
);
CREATE TABLE notifications (
 seq bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, id uuid NOT NULL UNIQUE DEFAULT gen_random_uuid(),
 recipient_id uuid NOT NULL REFERENCES users, patient_id uuid NOT NULL REFERENCES users,
 event_id uuid NOT NULL REFERENCES outbox_events, type text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(recipient_id,event_id)
);
CREATE INDEX notification_recipient ON notifications(recipient_id,seq);
CREATE TABLE audit_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, actor_id uuid REFERENCES users,
 action text NOT NULL, target_id text NOT NULL, request_id text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE login_attempts (key text PRIMARY KEY, attempts integer NOT NULL, expires_at timestamptz NOT NULL);
CREATE TABLE reminder_runs (id uuid NOT NULL DEFAULT gen_random_uuid(), assignment_id uuid REFERENCES assignments ON DELETE CASCADE, local_date date, PRIMARY KEY(assignment_id,local_date));
CREATE UNIQUE INDEX event_aggregate_type ON outbox_events(type,aggregate_id);
