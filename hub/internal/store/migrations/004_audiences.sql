-- Who may use a channel or feed. audience_json is {"mode","users","groups"}; an empty channel value means everyone,
-- and a NULL feed value means the feed uses its channel's audience (feeds.channel_id) or, without one, everyone.
ALTER TABLE channels ADD COLUMN audience_json TEXT NOT NULL DEFAULT '';
ALTER TABLE feeds ADD COLUMN channel_id TEXT;
ALTER TABLE feeds ADD COLUMN audience_json TEXT;

-- A group is a named list of external user ids. Members may not have paired yet.
CREATE TABLE groups (
    id         TEXT PRIMARY KEY,
    created_at INTEGER NOT NULL
);
CREATE TABLE group_members (
    group_id         TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    external_user_id TEXT NOT NULL,
    PRIMARY KEY (group_id, external_user_id)
);
CREATE INDEX group_members_user ON group_members(external_user_id);

-- Who ended a pairing: 'sender' (a kick), 'device' (an unpair) or 'relay'; '' for pairings revoked before this column existed.
ALTER TABLE pairings ADD COLUMN revoked_by TEXT NOT NULL DEFAULT '';
