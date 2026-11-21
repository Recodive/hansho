-- One active device per pairing and signing key: the same device registering again (an app reinstall keeps its
-- Secure Enclave key, and a join can race itself) must update its registration, not add a second one.
-- Older duplicates are retired first, keeping the newest registration of each.
UPDATE devices
   SET revoked_at = CAST(strftime('%s', 'now') AS INTEGER)
 WHERE revoked_at IS NULL
   AND rowid NOT IN (SELECT MAX(rowid) FROM devices WHERE revoked_at IS NULL GROUP BY pairing_id, sign_pub);

CREATE UNIQUE INDEX devices_active_pairing_sign ON devices(pairing_id, sign_pub) WHERE revoked_at IS NULL;
