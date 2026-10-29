-- Derived search index. memory_search_docs keeps only normalized terms; original
-- title and content stay in memories. The external-content FTS table is
-- synchronized by triggers and can be rebuilt from memory_search_docs at any
-- time. content_hash ties the document to the exact embedding input.
CREATE TABLE memory_search_docs (
    row_id        INTEGER PRIMARY KEY,
    memory_id     TEXT NOT NULL UNIQUE,
    title_terms   TEXT NOT NULL,
    content_terms TEXT NOT NULL,
    content_hash  TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (memory_id) REFERENCES memories(id) ON DELETE CASCADE
);

CREATE VIRTUAL TABLE memory_fts USING fts5(
    title_terms,
    content_terms,
    content = 'memory_search_docs',
    content_rowid = 'row_id',
    tokenize = 'unicode61'
);

CREATE TRIGGER memory_search_docs_ai AFTER INSERT ON memory_search_docs BEGIN
    INSERT INTO memory_fts(rowid, title_terms, content_terms)
    VALUES (new.row_id, new.title_terms, new.content_terms);
END;

CREATE TRIGGER memory_search_docs_ad AFTER DELETE ON memory_search_docs BEGIN
    INSERT INTO memory_fts(memory_fts, rowid, title_terms, content_terms)
    VALUES ('delete', old.row_id, old.title_terms, old.content_terms);
END;

CREATE TRIGGER memory_search_docs_au AFTER UPDATE ON memory_search_docs BEGIN
    INSERT INTO memory_fts(memory_fts, rowid, title_terms, content_terms)
    VALUES ('delete', old.row_id, old.title_terms, old.content_terms);
    INSERT INTO memory_fts(rowid, title_terms, content_terms)
    VALUES (new.row_id, new.title_terms, new.content_terms);
END;

-- L0 observation search index. An observation has no title; the derived row
-- stores the normalized content terms only. The original observation content
-- remains the only readable source.
CREATE TABLE observation_search_docs (
    row_id         INTEGER PRIMARY KEY,
    observation_id TEXT NOT NULL UNIQUE,
    content_terms  TEXT NOT NULL,
    FOREIGN KEY (observation_id) REFERENCES observations(id) ON DELETE CASCADE
);

CREATE VIRTUAL TABLE observation_fts USING fts5(
    content_terms,
    content = 'observation_search_docs',
    content_rowid = 'row_id',
    tokenize = 'unicode61'
);

CREATE TRIGGER observation_search_docs_ai AFTER INSERT ON observation_search_docs BEGIN
    INSERT INTO observation_fts(rowid, content_terms)
    VALUES (new.row_id, new.content_terms);
END;

CREATE TRIGGER observation_search_docs_ad AFTER DELETE ON observation_search_docs BEGIN
    INSERT INTO observation_fts(observation_fts, rowid, content_terms)
    VALUES ('delete', old.row_id, old.content_terms);
END;

CREATE TRIGGER observation_search_docs_au AFTER UPDATE ON observation_search_docs BEGIN
    INSERT INTO observation_fts(observation_fts, rowid, content_terms)
    VALUES ('delete', old.row_id, old.content_terms);
    INSERT INTO observation_fts(rowid, content_terms)
    VALUES (new.row_id, new.content_terms);
END;
