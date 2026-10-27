CREATE TABLE email_jobs (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 source_type text NOT NULL CHECK(source_type IN ('notification','invitation')),
 source_id uuid NOT NULL,
 recipient_key text NOT NULL,
 recipient_id uuid REFERENCES users,
 recipient_email text NOT NULL,
 notification_id uuid REFERENCES notifications(id),
 invitation_id uuid REFERENCES invitations,
 encrypted_payload bytea,
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','sending','sent','cancelled')),
 attempts integer NOT NULL DEFAULT 0,
 available_at timestamptz NOT NULL DEFAULT now(),
 lease_token uuid,
 leased_until timestamptz,
 accepted_at timestamptz,
 last_error_code text,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(source_type,source_id,recipient_key)
);
CREATE INDEX email_jobs_pending ON email_jobs(available_at,created_at) WHERE status IN ('pending','sending');
COMMENT ON TABLE email_jobs IS 'Durable secondary email delivery; SMTP acceptance is not clinician acknowledgement. Invitation payloads are AES-GCM encrypted.';
CREATE TABLE email_delivery_state (
 id boolean PRIMARY KEY DEFAULT true CHECK(id),
 notification_start_seq bigint NOT NULL
);
INSERT INTO email_delivery_state(id,notification_start_seq) SELECT true,COALESCE(max(seq),0) FROM notifications;
ALTER TABLE email_jobs ADD COLUMN priority integer NOT NULL DEFAULT 10;
