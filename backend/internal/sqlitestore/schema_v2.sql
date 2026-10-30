-- Schema version 2, from version 1. Applied by sqlitestore.Open in the same
-- write transaction as version 1 on a new file, so every database reaches
-- version 2 down this one path. Open then builds the index tables below from
-- memory_events (index.go).

-- A remember's keywords: a JSON array of strings, or NULL for none. Part of
-- the memory's value, so they live in the history.
ALTER TABLE memory_events ADD COLUMN keywords TEXT
    CHECK (keywords IS NULL OR kind = 'remember');

-- Everything below is a derived index over memory_events, never the source
-- of truth: rows here are replaced and deleted as memories change, and can
-- be rebuilt from the history at any time.

-- One row per current memory. id is the memory's rowid in memory_fts and
-- memory_words, and in the optional trigram index (trigram.go), which is
-- not part of this schema; seq is its latest event.
CREATE TABLE memories (
    id        INTEGER PRIMARY KEY,
    memory_id TEXT    NOT NULL UNIQUE,
    seq       INTEGER NOT NULL
);

-- Full-text index over each current memory. Contentless: the text lives in
-- memory_events. Diacritics are kept: "zoe" does not find "Zoë".
CREATE VIRTUAL TABLE memory_fts USING fts5(
    content, keywords,
    content = '', contentless_delete = 1,
    tokenize = 'unicode61 remove_diacritics 0'
);

-- The case-folded words of each current memory's content, with how each is
-- written there (related.go): bit 1 lower-case, bit 2 upper-case (two or
-- more letters), bit 4 upper-case other than as the first word.
CREATE TABLE memory_words (
    word  TEXT    NOT NULL,
    id    INTEGER NOT NULL,
    flags INTEGER NOT NULL,
    PRIMARY KEY (word, id)
) WITHOUT ROWID;

CREATE INDEX memory_words_id ON memory_words (id);

-- The last memory_events seq the index tables reflect.
CREATE TABLE index_state (
    one INTEGER PRIMARY KEY CHECK (one = 1),
    seq INTEGER NOT NULL
);

INSERT INTO index_state (one, seq) VALUES (1, 0);

PRAGMA user_version = 2;
