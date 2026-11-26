-- Messages of one send share a send_id. page_ack says how a page is acknowledged: 'any' (the first acknowledgement by any
-- recipient of the send stops it for all of them) or 'each' (every recipient acknowledges for themselves). Pages that
-- exist when this runs finish as 'each'. acked_by is the external user id that acknowledged the message and
-- acked_by_name that person's display name at that moment ('' when they had none, or before this).
ALTER TABLE messages ADD COLUMN send_id TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN page_ack TEXT NOT NULL DEFAULT 'each';
ALTER TABLE messages ADD COLUMN acked_by TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN acked_by_name TEXT NOT NULL DEFAULT '';
CREATE INDEX messages_send ON messages(send_id) WHERE send_id != '';

-- What other people see instead of a user's external user id. One per user, shared by their devices; no row = none.
CREATE TABLE user_profiles (
    external_user_id TEXT PRIMARY KEY,
    display_name     TEXT NOT NULL,
    updated_at       INTEGER NOT NULL
);
