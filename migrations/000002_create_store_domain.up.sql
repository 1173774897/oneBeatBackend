-- Store domain: Huawei-account sessions, free (redemption/admin) entitlements
-- and their audit trail. Paid Huawei purchases are verified and delivered on the
-- device and are never stored here (cross-border transfer, 2026-11-03), so
-- there are no order, subscription, token or notification tables.
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- HMAC of the Huawei UnionID with a server-side pepper; never the plain ID.
    -- Account deletion replaces it with random bytes to detach the identity.
    huawei_union_id_hash bytea NOT NULL UNIQUE,
    status text NOT NULL DEFAULT 'ACTIVE'
        CHECK (status IN ('ACTIVE', 'DISABLED', 'DELETED')),
    last_login_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,
    CONSTRAINT users_deleted_at_matches_status
        CHECK ((status = 'DELETED') = (deleted_at IS NOT NULL))
);

CREATE TABLE entitlement_grants (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id),
    entitlement_key text NOT NULL,
    -- Purchase sources are deliberately not representable.
    source_type text NOT NULL CHECK (source_type IN ('REDEMPTION', 'ADMIN')),
    source_ref uuid NOT NULL,
    starts_at timestamptz NOT NULL,
    ends_at timestamptz,
    revoked_at timestamptz,
    revoke_reason text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source_type, source_ref, entitlement_key),
    CHECK (ends_at IS NULL OR ends_at > starts_at),
    CHECK (revoked_at IS NOT NULL OR revoke_reason IS NULL)
);

-- Bootstrap and account deletion both read a user's unrevoked grants by time window.
CREATE INDEX entitlement_grants_active_window_idx
    ON entitlement_grants(user_id, ends_at, starts_at)
    WHERE revoked_at IS NULL;

CREATE TABLE redemptions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id),
    campaign_key text NOT NULL,
    -- Configured phrase identifier, never the redemption plaintext.
    code_key text NOT NULL,
    config_version text NOT NULL,
    idempotency_key uuid NOT NULL,
    account_sequence integer NOT NULL CHECK (account_sequence > 0),
    chain_id uuid NOT NULL,
    chain_anchor_at timestamptz NOT NULL,
    month_ordinal integer NOT NULL CHECK (month_ordinal > 0),
    redeemed_at timestamptz NOT NULL,
    grant_starts_at timestamptz NOT NULL,
    grant_ends_at timestamptz NOT NULL,
    entitlement_grant_id uuid NOT NULL UNIQUE REFERENCES entitlement_grants(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, idempotency_key),
    CHECK (grant_ends_at > grant_starts_at)
);

-- Approximate campaign-wide totals count this fact table directly (no shared counter row).
CREATE INDEX redemptions_campaign_user_idx ON redemptions(campaign_key, user_id);
CREATE INDEX redemptions_chain_idx ON redemptions(chain_id, month_ordinal);
-- Active gift-chain lookup without a campaign-wide lock.
CREATE INDEX redemptions_user_grant_end_idx ON redemptions(user_id, grant_ends_at DESC);

CREATE TABLE redemption_account_usage (
    campaign_key text NOT NULL,
    user_id uuid NOT NULL REFERENCES users(id),
    redeemed_count integer NOT NULL DEFAULT 0 CHECK (redeemed_count >= 0),
    last_redeemed_at timestamptz,
    PRIMARY KEY (campaign_key, user_id)
);

CREATE TABLE entitlement_audit_logs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id),
    entitlement_key text NOT NULL,
    -- Redemption writes GRANTED/EXTENDED; account deletion writes REVOKED.
    event_type text NOT NULL CHECK (event_type IN ('GRANTED', 'EXTENDED', 'REVOKED')),
    source_type text NOT NULL,
    source_ref uuid,
    before_snapshot jsonb,
    after_snapshot jsonb,
    request_id text,
    occurred_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX entitlement_audit_logs_user_time_idx
    ON entitlement_audit_logs(user_id, occurred_at DESC);
CREATE INDEX entitlement_audit_logs_source_idx
    ON entitlement_audit_logs(source_type, source_ref)
    WHERE source_ref IS NOT NULL;
