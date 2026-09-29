CREATE TABLE store_metadata (
    key text PRIMARY KEY,
    value text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO store_metadata (key, value)
VALUES ('schema_initialized', 'true');

