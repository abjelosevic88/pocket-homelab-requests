-- The person's Jellyfin account, for the Jellyseerr-compatible API: Jellyseerr
-- clients (Pocket Watch, Pocket Core) find a person by their Jellyfin user id.
ALTER TABLE users ADD COLUMN jellyfin_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN jellyfin_username TEXT NOT NULL DEFAULT '';
