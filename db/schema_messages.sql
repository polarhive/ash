-- Messages table for storing all messages
CREATE TABLE IF NOT EXISTS messages (
    id TEXT PRIMARY KEY,
    room_id TEXT,
    sender TEXT,
    ts_ms INTEGER,
    body TEXT,
    msgtype TEXT,
    raw_json TEXT
);

-- Links table for storing extracted URLs from messages
CREATE TABLE IF NOT EXISTS links (
    message_id TEXT,
    url TEXT,
    idx INTEGER,
    title TEXT,
    ts_ms INTEGER,
    PRIMARY KEY (message_id, url, idx)
);

-- Quotewall table for storing logged sus moments
CREATE TABLE IF NOT EXISTS quotewall (
    id TEXT PRIMARY KEY,
    room_id TEXT,
    target_user TEXT,
    target_message TEXT,
    target_ts_ms INTEGER,
    logged_by TEXT,
    logged_at_ms INTEGER,
    UNIQUE(room_id, target_user, target_ts_ms)
);

-- Reactions table for tracking emoji reactions
CREATE TABLE IF NOT EXISTS reactions (
    message_id TEXT,
    room_id TEXT,
    emoji TEXT,
    reactor TEXT,
    created_at_ms INTEGER,
    PRIMARY KEY (message_id, emoji, reactor)
);

CREATE INDEX IF NOT EXISTS idx_reactions_room_ts ON reactions(room_id, created_at_ms);
CREATE INDEX IF NOT EXISTS idx_reactions_msg ON reactions(message_id);
