CREATE TABLE IF NOT EXISTS memories (
    id          TEXT PRIMARY KEY,
    content     TEXT NOT NULL,
    importance  REAL DEFAULT 0.5,
    server_id   TEXT NOT NULL,
    user_id     TEXT,
    channel_id  TEXT,
    created_at  DATETIME NOT NULL,
    updated_at  DATETIME NOT NULL,
    forgotten   INTEGER DEFAULT 0
);

CREATE TABLE IF NOT EXISTS embeddings (
    memory_id   TEXT PRIMARY KEY REFERENCES memories(id) ON DELETE CASCADE,
    vector      BLOB NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_memories_server ON memories(server_id);
CREATE INDEX IF NOT EXISTS idx_memories_user   ON memories(server_id, user_id);

CREATE TABLE IF NOT EXISTS conversations (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    channel_id TEXT NOT NULL,
    user_msg   TEXT NOT NULL,
    tool_calls TEXT,  -- JSON array [{name, result}]
    response   TEXT NOT NULL,
    ts         DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_conv_channel ON conversations(channel_id);
