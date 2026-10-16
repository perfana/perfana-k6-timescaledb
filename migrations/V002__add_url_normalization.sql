-- Migration: Add URL normalization support
-- Description: Adds url_hash column to requests_raw and requests_error tables,
--              and creates url_patterns table for storing normalized URL patterns.

-- Add url_hash column to requests_raw table
ALTER TABLE requests_raw ADD COLUMN IF NOT EXISTS url_hash TEXT;

-- Add url_hash column to requests_error table
ALTER TABLE requests_error ADD COLUMN IF NOT EXISTS url_hash TEXT;

-- Create url_patterns table for normalized URL deduplication
CREATE TABLE IF NOT EXISTS url_patterns (
    url_hash TEXT NOT NULL,
    system_under_test TEXT NOT NULL,
    test_environment TEXT NOT NULL,
    normalized_url TEXT NOT NULL,
    original_example TEXT,
    first_seen TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (url_hash, system_under_test, test_environment)
);

-- Create index on url_hash for faster lookups from requests_raw and requests_error
CREATE INDEX IF NOT EXISTS idx_requests_raw_url_hash ON requests_raw (url_hash);
CREATE INDEX IF NOT EXISTS idx_requests_error_url_hash ON requests_error (url_hash);

-- Create index on normalized_url for pattern searches
CREATE INDEX IF NOT EXISTS idx_url_patterns_normalized_url ON url_patterns (normalized_url);

-- Comment on table and columns
COMMENT ON TABLE url_patterns IS 'Stores normalized URL patterns with dynamic segments masked (UUIDs, IDs, tokens, query values)';
COMMENT ON COLUMN url_patterns.url_hash IS 'MD5 hash of the normalized URL';
COMMENT ON COLUMN url_patterns.system_under_test IS 'System under test identifier - part of composite key';
COMMENT ON COLUMN url_patterns.test_environment IS 'Test environment identifier - part of composite key';
COMMENT ON COLUMN url_patterns.normalized_url IS 'URL with dynamic segments replaced by placeholders ({uuid}, {id}, {token}, {value})';
COMMENT ON COLUMN url_patterns.original_example IS 'First original URL seen that matches this pattern';
COMMENT ON COLUMN url_patterns.first_seen IS 'Timestamp when this URL pattern was first encountered';
