CREATE TABLE clinical_evidence_reviews (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 patient_id uuid NOT NULL REFERENCES users(id),
 context_version_id uuid NOT NULL REFERENCES clinical_context_versions(id),
 doctor_id uuid NOT NULL REFERENCES users(id),
 diagnoses text NOT NULL CHECK(length(diagnoses) BETWEEN 1 AND 5000),
 diagnosis_hash text NOT NULL,
 status text NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','searching','analyzing','ready','failed','model_unavailable')),
 pubmed_query text,
 sources jsonb NOT NULL DEFAULT '[]',
 result jsonb,
 provider text,
 model text,
 prompt_version text NOT NULL DEFAULT 'clinical-evidence-v1',
 error_code text,
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 5),
 created_at timestamptz NOT NULL DEFAULT now(),
 started_at timestamptz,
 completed_at timestamptz,
 UNIQUE(context_version_id, diagnosis_hash, prompt_version)
);

CREATE INDEX clinical_evidence_reviews_patient_created
 ON clinical_evidence_reviews(patient_id, created_at DESC, id DESC);
CREATE INDEX clinical_evidence_reviews_queue
 ON clinical_evidence_reviews(status, created_at)
 WHERE status IN ('queued','searching','analyzing');

COMMENT ON TABLE clinical_evidence_reviews IS 'Doctor-only, source-bound AI evidence drafts; never an automatic diagnosis or prescription.';
COMMENT ON COLUMN clinical_evidence_reviews.sources IS 'Immutable PubMed article snapshot supplied to the model.';
COMMENT ON COLUMN clinical_evidence_reviews.result IS 'Structured clinician draft whose citations must reference sources PMIDs.';
