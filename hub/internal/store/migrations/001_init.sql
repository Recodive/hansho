-- API keys are stored as sha256 only.
CREATE TABLE api_keys (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    hash       TEXT NOT NULL UNIQUE,
    name       TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    revoked_at INTEGER
);

-- Pairing tickets: the ticket column holds sha256(ticket), never the ticket itself.
CREATE TABLE tickets (
    ticket           TEXT PRIMARY KEY,
    external_user_id TEXT NOT NULL,
    expires_at       INTEGER NOT NULL,
    used_at          INTEGER
);

-- key_wrapped is the pairing key AES-256-GCM-wrapped with HUB_MASTER_KEY.
CREATE TABLE pairings (
    id               TEXT PRIMARY KEY,
    external_user_id TEXT NOT NULL,
    key_wrapped      BLOB NOT NULL,
    created_at       INTEGER NOT NULL,
    revoked_at       INTEGER
);
CREATE INDEX pairings_user ON pairings(external_user_id);

-- sealed_* are relay-sealed delivery tokens; '' means not registered / cleared.
CREATE TABLE devices (
    id                 TEXT PRIMARY KEY,
    pairing_id         TEXT NOT NULL REFERENCES pairings(id),
    sign_pub           BLOB NOT NULL,
    platform           TEXT NOT NULL,
    name               TEXT NOT NULL,
    sealed_alert       TEXT NOT NULL DEFAULT '',
    sealed_background  TEXT NOT NULL DEFAULT '',
    sealed_pushtostart TEXT NOT NULL DEFAULT '',
    sealed_widgets     TEXT NOT NULL DEFAULT '',
    created_at         INTEGER NOT NULL,
    revoked_at         INTEGER
);
CREATE INDEX devices_pairing ON devices(pairing_id);

CREATE TABLE channels (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    default_level TEXT NOT NULL,
    max_level     TEXT NOT NULL,
    sound         TEXT NOT NULL DEFAULT '',
    template_id   TEXT NOT NULL DEFAULT '',
    thread        TEXT NOT NULL DEFAULT '',
    actions_json  TEXT NOT NULL DEFAULT '[]',
    deleted_at    INTEGER
);

CREATE TABLE templates (
    id        TEXT PRIMARY KEY,
    version   INTEGER NOT NULL,
    body_json TEXT NOT NULL
);

CREATE TABLE sounds (
    id           TEXT PRIMARY KEY,
    filename     TEXT NOT NULL,
    content_type TEXT NOT NULL,
    data         BLOB NOT NULL,
    sha256       TEXT NOT NULL
);

-- payload_wrapped is the plaintext message JSON wrapped with the master key,
-- kept so pages can be re-sealed for resends.
CREATE TABLE messages (
    id               TEXT PRIMARY KEY,
    external_user_id TEXT NOT NULL,
    channel_id       TEXT NOT NULL,
    level            TEXT NOT NULL,
    status           TEXT NOT NULL,
    page_interval_s  INTEGER NOT NULL DEFAULT 0,
    page_max_retries INTEGER NOT NULL DEFAULT 0,
    attempts         INTEGER NOT NULL DEFAULT 0,
    next_attempt_at  INTEGER NOT NULL DEFAULT 0,
    created_at       INTEGER NOT NULL,
    acked_at         INTEGER,
    collapse_id      TEXT NOT NULL DEFAULT '',
    sound            TEXT NOT NULL DEFAULT '',
    payload_wrapped  BLOB NOT NULL DEFAULT x''
);
CREATE INDEX messages_status ON messages(status, next_attempt_at);
CREATE INDEX messages_user ON messages(external_user_id);

CREATE TABLE deliveries (
    message_id TEXT NOT NULL REFERENCES messages(id),
    device_id  TEXT NOT NULL,
    apns_id    TEXT NOT NULL DEFAULT '',
    status     TEXT NOT NULL,
    error      TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (message_id, device_id)
);

-- data_json/priority/alert_json hold the latest update; update_seq counts
-- updates. A token whose sent_seq is behind update_seq still has to receive the
-- current state (e.g. an update sent before its token was registered).
CREATE TABLE activities (
    id               TEXT PRIMARY KEY,
    external_user_id TEXT NOT NULL,
    channel_id       TEXT NOT NULL,
    template_id      TEXT NOT NULL,
    started_at       INTEGER NOT NULL,
    ends_at          INTEGER NOT NULL,
    ended_at         INTEGER,
    data_json        TEXT NOT NULL DEFAULT 'null',
    priority         INTEGER NOT NULL DEFAULT 5,
    alert_json       TEXT NOT NULL DEFAULT '',
    update_seq       INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX activities_ends ON activities(ended_at, ends_at);

CREATE TABLE activity_tokens (
    activity_id   TEXT NOT NULL REFERENCES activities(id),
    device_id     TEXT NOT NULL,
    sealed_update TEXT NOT NULL,
    sent_seq      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (activity_id, device_id)
);

-- external_user_id NULL = feed for all paired users.
CREATE TABLE feeds (
    id               TEXT NOT NULL,
    external_user_id TEXT,
    template_id      TEXT NOT NULL,
    data_json        TEXT NOT NULL,
    updated_at       INTEGER NOT NULL
);
CREATE UNIQUE INDEX feeds_key ON feeds(id, COALESCE(external_user_id, ''));

CREATE TABLE seen_nonces (
    nonce      TEXT PRIMARY KEY,
    expires_at INTEGER NOT NULL
);
CREATE INDEX seen_nonces_exp ON seen_nonces(expires_at);

CREATE TABLE webhook_outbox (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    event           TEXT NOT NULL,
    payload_json    TEXT NOT NULL,
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL,
    created_at      INTEGER NOT NULL,
    delivered_at    INTEGER,
    dropped_at      INTEGER
);
CREATE INDEX webhook_due ON webhook_outbox(delivered_at, dropped_at, next_attempt_at);
