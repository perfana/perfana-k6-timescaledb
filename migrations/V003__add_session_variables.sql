-- Migration: Add session_variables capture to requests_error
-- Description: Adds a nullable jsonb column holding a snapshot of the failing virtual
--              user's JMeter session variables. Populated only for failed samples when
--              the listener's saveSessionVariables option is enabled.
--
-- NOTE: This is a DEV/TEST MIRROR of the canonical schema, which is owned by the Perfana
-- repo (packages/shared/src/database/migrations). This change has already landed there;
-- this file keeps local and integration test databases in sync. Do not diverge from the
-- canonical DDL.

ALTER TABLE requests_error ADD COLUMN IF NOT EXISTS session_variables jsonb;

COMMENT ON COLUMN requests_error.session_variables IS
    'Snapshot of the failing virtual user JMeter session variables (deny-list filtered). NULL for success rows and when capture is disabled.';
