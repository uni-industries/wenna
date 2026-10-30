-- Schema version 1 (PRAGMA user_version). Applied by sqlitestore.Open when
-- user_version is 0, inside a write transaction, so processes opening the
-- same file at once apply it once.
-- Ids are ULIDs (package id), stored as canonical 26-character text.

-- Memory history: append-only. The current value of a memory is its latest
-- event; a 'forget' event as the latest means the memory is gone. Rows are
-- never updated or deleted (CLAUDE.md § Decisions).
CREATE TABLE memory_events (
    seq        INTEGER PRIMARY KEY AUTOINCREMENT,
    memory_id  TEXT    NOT NULL CHECK (length(memory_id) = 26),
    kind       TEXT    NOT NULL CHECK (kind IN ('remember', 'forget')),
    content    TEXT,
    -- Unix time in nanoseconds, UTC.
    created_at INTEGER NOT NULL,
    CHECK ((kind = 'remember') = (content IS NOT NULL))
);

CREATE INDEX memory_events_memory_seq ON memory_events (memory_id, seq DESC);

PRAGMA user_version = 1;
