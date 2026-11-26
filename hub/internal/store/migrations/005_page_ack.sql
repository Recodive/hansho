-- Messages of one send share a send_id. page_ack says how a page is acknowledged: 'any' (the first acknowledgement by any
-- recipient of the send stops it for all of them) or 'each' (every recipient acknowledges for themselves). Pages that
-- exist when this runs finish as 'each'. acked_by is the external user id that acknowledged the message ('' before this).
ALTER TABLE messages ADD COLUMN send_id TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN page_ack TEXT NOT NULL DEFAULT 'each';
ALTER TABLE messages ADD COLUMN acked_by TEXT NOT NULL DEFAULT '';
-- show_acker: the sender allows the cancel banner of the other recipients to name who acknowledged (their external user id).
ALTER TABLE messages ADD COLUMN show_acker INTEGER NOT NULL DEFAULT 0;
CREATE INDEX messages_send ON messages(send_id) WHERE send_id != '';
