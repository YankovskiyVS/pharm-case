ALTER TABLE users ADD COLUMN notification_read_seq bigint NOT NULL DEFAULT 0;
ALTER TABLE reviews ADD COLUMN period_start date;
ALTER TABLE reviews ADD COLUMN period_end date;
ALTER TABLE reviews ADD COLUMN source_entry_ids uuid[] NOT NULL DEFAULT '{}';
