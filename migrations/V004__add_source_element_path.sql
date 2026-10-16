-- Migration: Add source_element_path to requests_raw
-- Description: Records where in the test plan a request came from, as a JSON array of
--              {name, class, occurrence} entries from the Thread Group down to the sampler. The
--              occurrence disambiguates identically named siblings, which nothing else on the
--              row can: two Transaction Controllers both called "checkout" in different parts of
--              a plan produce identical request rows today. Lets a consumer show the plan path
--              of a request and group requests by the branch of the plan that issued them.
--
--              NULL when the engine does not attach the path (stock Apache JMeter, or a
--              BreakTest build without listener sample metadata).
--
-- NOTE: This is a DEV/TEST MIRROR of the canonical schema, which is owned by the Perfana repo
-- (packages/shared/src/database/migrations). Apply the canonical migration to target environments
-- before deploying this plugin; this file keeps local and integration test databases in sync.
-- Do not diverge from the canonical DDL. See docs/source-element-path-schema-writeup.md.

ALTER TABLE requests_raw ADD COLUMN IF NOT EXISTS source_element_path jsonb;

COMMENT ON COLUMN requests_raw.source_element_path IS
    'Test plan path of this request, outermost first: [{"name","class","occurrence"}] from the Thread Group down to the sampler. NULL when the engine does not attach it or the column predates the running plugin.';
