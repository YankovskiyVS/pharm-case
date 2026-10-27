ALTER TABLE alerts ALTER COLUMN signal_id DROP NOT NULL;
ALTER TABLE alerts ADD COLUMN rule_version text;
ALTER TABLE alerts ADD COLUMN period_start date;
ALTER TABLE alerts ADD COLUMN period_end date;
ALTER TABLE alerts ADD COLUMN source_entry_ids uuid[] NOT NULL DEFAULT '{}';
ALTER TABLE alerts ADD COLUMN details jsonb NOT NULL DEFAULT '{}';
ALTER TABLE alerts ADD COLUMN severity text NOT NULL DEFAULT 'critical' CHECK(severity IN ('warning','critical'));
ALTER TABLE alerts ADD COLUMN title text NOT NULL DEFAULT 'Положительный ответ о суицидальных мыслях';
ALTER TABLE alerts ADD COLUMN reason text NOT NULL DEFAULT 'Требуется немедленная оценка врача';
ALTER TABLE alerts ADD COLUMN metric text;
ALTER TABLE alerts ADD COLUMN baseline_value numeric;
ALTER TABLE alerts ADD COLUMN current_value numeric;
ALTER TABLE alerts ADD COLUMN delta numeric;
ALTER TABLE alerts ADD CONSTRAINT alerts_type_check CHECK(type IN ('SUICIDE','DETERIORATION'));

ALTER TABLE alerts ADD CONSTRAINT alerts_source_check CHECK (
 (type='SUICIDE' AND signal_id IS NOT NULL AND rule_version IS NULL AND period_start IS NULL AND period_end IS NULL)
 OR
 (type='DETERIORATION' AND signal_id IS NULL AND rule_version IS NOT NULL AND period_start IS NOT NULL AND period_end IS NOT NULL AND period_end>=period_start AND cardinality(source_entry_ids)=3 AND severity='warning' AND metric='mood' AND baseline_value IS NOT NULL AND current_value IS NOT NULL AND delta>=3)
);

CREATE UNIQUE INDEX alerts_deterioration_window
 ON alerts(patient_id,type,rule_version,period_end)
 WHERE type='DETERIORATION';

COMMENT ON COLUMN alerts.rule_version IS 'Versioned clinical indicator definition; NULL for immediate suicide-risk signals.';
COMMENT ON COLUMN alerts.details IS 'Explainable indicator inputs, never a diagnosis.';
