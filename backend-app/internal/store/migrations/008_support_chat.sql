CREATE TABLE support_chat_turns (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 patient_id uuid NOT NULL REFERENCES users,
 request_key uuid NOT NULL,
 request_hash text NOT NULL,
 user_message_id uuid NOT NULL UNIQUE DEFAULT gen_random_uuid(),
 assistant_message_id uuid NOT NULL UNIQUE DEFAULT gen_random_uuid(),
 user_text text NOT NULL CHECK(length(user_text) BETWEEN 1 AND 2000),
 assistant_text text NOT NULL CHECK(length(assistant_text) BETWEEN 1 AND 12000),
 provider text NOT NULL,
 model text NOT NULL,
 safety text NOT NULL CHECK(safety IN ('standard','crisis','medical_boundary','provider_unavailable')),
 external_consent boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(patient_id,request_key)
);
CREATE INDEX support_chat_history ON support_chat_turns(patient_id,created_at DESC,id DESC);
COMMENT ON TABLE support_chat_turns IS 'Patient-private immutable supportive bot turns; separate from doctor messages. Explicit provider provenance and consent. Never a diagnosis or medical order.';
