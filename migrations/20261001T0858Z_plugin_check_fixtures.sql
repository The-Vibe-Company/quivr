-- Check a plugin with its own fixtures (THE-807, Spec 5). A registration
-- request may carry the files of the plugin's fixtures folder, by path, so a
-- connector or a normalizer for any media type can be certified. They are
-- stored with the registration, so the check that produced its report can run
-- again, and the digest of each request's fixtures is kept with its
-- idempotency key, so a key replayed with other fixtures is refused.
ALTER TABLE plugin_registrations ADD COLUMN check_fixtures jsonb;
ALTER TABLE plugin_registration_requests ADD COLUMN fixtures_digest text NOT NULL DEFAULT '';
