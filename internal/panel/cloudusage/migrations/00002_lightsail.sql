-- +goose Up
ALTER TABLE cloud_usage_sample ADD COLUMN inbound_bytes INTEGER CHECK(inbound_bytes >= 0);
ALTER TABLE cloud_usage_resource ADD COLUMN allowance_gb INTEGER NOT NULL DEFAULT 0 CHECK(allowance_gb >= 0);
