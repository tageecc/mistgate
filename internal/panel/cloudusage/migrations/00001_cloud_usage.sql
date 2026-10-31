-- +goose Up
CREATE TABLE cloud_usage_account (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    provider TEXT NOT NULL,
    account_id TEXT NOT NULL,
    credentials BLOB,
    revision INTEGER NOT NULL DEFAULT 1,
    billing_enabled INTEGER NOT NULL DEFAULT 0,
    reconnect INTEGER NOT NULL DEFAULT 0,
    bill_attempt INTEGER NOT NULL DEFAULT 0,
    UNIQUE(provider, account_id)
) STRICT;

CREATE TABLE cloud_usage_resource (
    id TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES cloud_usage_account(id),
    region TEXT NOT NULL,
    instance_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    scanned_until INTEGER NOT NULL DEFAULT 0,
    UNIQUE(account_id, region, instance_id)
) STRICT;

CREATE TABLE cloud_usage_binding (
    node_id TEXT PRIMARY KEY REFERENCES node(id) ON DELETE CASCADE,
    resource_id TEXT NOT NULL UNIQUE REFERENCES cloud_usage_resource(id),
    threshold_bytes INTEGER NOT NULL DEFAULT 0 CHECK(threshold_bytes >= 0),
    threshold_amount TEXT NOT NULL DEFAULT '',
    threshold_currency TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE TABLE cloud_usage_sample (
    resource_id TEXT NOT NULL REFERENCES cloud_usage_resource(id),
    interval_end INTEGER NOT NULL,
    outbound_bytes INTEGER NOT NULL CHECK(outbound_bytes >= 0),
    PRIMARY KEY(resource_id, interval_end)
) STRICT;

-- Each row is an entire, successfully paginated monthly bill snapshot. Replace, never add to it.
CREATE TABLE cloud_usage_bill (
    account_id TEXT NOT NULL REFERENCES cloud_usage_account(id),
    month TEXT NOT NULL,
    fetched_at INTEGER NOT NULL,
    entries TEXT NOT NULL CHECK(json_valid(entries)),
    PRIMARY KEY(account_id, month)
) STRICT;
