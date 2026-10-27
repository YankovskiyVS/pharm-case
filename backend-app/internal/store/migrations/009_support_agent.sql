ALTER TABLE support_chat_turns ADD COLUMN agent jsonb CHECK(agent IS NULL OR jsonb_typeof(agent)='object');
COMMENT ON COLUMN support_chat_turns.agent IS 'Immutable bounded-agent public execution summary and versioned state snapshot; no chain of thought, opaque reasoning or provider credentials. NULL for legacy nonagent turns.';
