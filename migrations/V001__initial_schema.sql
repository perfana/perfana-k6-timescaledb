-- Migration: Initial TimescaleDB schema for JMeter backend listener
-- Description: Creates base tables for storing JMeter test results

-- Enable TimescaleDB extension (if not already enabled)
CREATE EXTENSION IF NOT EXISTS timescaledb;

-- Requests raw table - stores individual sampler results
CREATE TABLE IF NOT EXISTS requests_raw (
    time TIMESTAMPTZ NOT NULL,
    test_run_id TEXT NOT NULL,
    system_under_test TEXT NOT NULL,
    test_environment TEXT NOT NULL,
    scenario_name TEXT,
    location TEXT,
    transaction_name TEXT,
    sampler_name TEXT NOT NULL,
    success BOOLEAN NOT NULL,
    request_size INTEGER,
    response_size INTEGER,
    response_code TEXT,
    response_connect_time INTEGER,
    response_latency INTEGER,
    response_time INTEGER
);

-- Transactions table - stores transaction (controller) results
CREATE TABLE IF NOT EXISTS transactions (
    time TIMESTAMPTZ NOT NULL,
    test_run_id TEXT NOT NULL,
    system_under_test TEXT NOT NULL,
    test_environment TEXT NOT NULL,
    scenario_name TEXT,
    location TEXT,
    transaction_name TEXT NOT NULL,
    success BOOLEAN NOT NULL,
    request_size INTEGER,
    response_size INTEGER,
    response_time INTEGER
);

-- Request errors table - stores detailed error information for failed requests
CREATE TABLE IF NOT EXISTS requests_error (
    time TIMESTAMPTZ NOT NULL,
    test_run_id TEXT NOT NULL,
    system_under_test TEXT NOT NULL,
    test_environment TEXT NOT NULL,
    scenario_name TEXT,
    location TEXT,
    node_name TEXT NOT NULL,
    transaction_name TEXT NOT NULL,
    sampler_name TEXT NOT NULL,
    response_code TEXT,
    response_time INTEGER,
    connection_time INTEGER,
    url TEXT,
    assertions TEXT,
    response_message TEXT,
    request_headers TEXT,
    response_headers TEXT,
    response_data TEXT,
    random_id INTEGER
);

-- Virtual users table - stores thread count metrics over time
CREATE TABLE IF NOT EXISTS virtual_users (
    time TIMESTAMPTZ NOT NULL,
    test_run_id TEXT NOT NULL,
    system_under_test TEXT NOT NULL,
    test_environment TEXT NOT NULL,
    scenario_name TEXT,
    location TEXT,
    node_name TEXT NOT NULL,
    active_threads INTEGER,
    started_threads INTEGER,
    finished_threads INTEGER
);

-- Convert tables to hypertables for time-series optimization
SELECT create_hypertable('requests_raw', 'time', if_not_exists => TRUE);
SELECT create_hypertable('transactions', 'time', if_not_exists => TRUE);
SELECT create_hypertable('requests_error', 'time', if_not_exists => TRUE);
SELECT create_hypertable('virtual_users', 'time', if_not_exists => TRUE);

-- Create indexes for common query patterns
CREATE INDEX IF NOT EXISTS idx_requests_raw_test_run ON requests_raw (test_run_id, time DESC);
CREATE INDEX IF NOT EXISTS idx_requests_raw_sut_env ON requests_raw (system_under_test, test_environment, time DESC);
CREATE INDEX IF NOT EXISTS idx_requests_raw_transaction ON requests_raw (transaction_name, time DESC);
CREATE INDEX IF NOT EXISTS idx_requests_raw_sampler ON requests_raw (sampler_name, time DESC);

CREATE INDEX IF NOT EXISTS idx_transactions_test_run ON transactions (test_run_id, time DESC);
CREATE INDEX IF NOT EXISTS idx_transactions_sut_env ON transactions (system_under_test, test_environment, time DESC);
CREATE INDEX IF NOT EXISTS idx_transactions_name ON transactions (transaction_name, time DESC);

CREATE INDEX IF NOT EXISTS idx_requests_error_test_run ON requests_error (test_run_id, time DESC);
CREATE INDEX IF NOT EXISTS idx_requests_error_sut_env ON requests_error (system_under_test, test_environment, time DESC);
CREATE INDEX IF NOT EXISTS idx_requests_error_response_code ON requests_error (response_code, time DESC);

CREATE INDEX IF NOT EXISTS idx_virtual_users_test_run ON virtual_users (test_run_id, time DESC);
CREATE INDEX IF NOT EXISTS idx_virtual_users_sut_env ON virtual_users (system_under_test, test_environment, time DESC);

-- Add table comments
COMMENT ON TABLE requests_raw IS 'Individual HTTP sampler results from JMeter tests';
COMMENT ON TABLE transactions IS 'Transaction controller results aggregating multiple samplers';
COMMENT ON TABLE requests_error IS 'Detailed error information for failed requests including headers and response body';
COMMENT ON TABLE virtual_users IS 'Thread count metrics over time for load profile tracking';
