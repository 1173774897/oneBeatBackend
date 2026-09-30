CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    huawei_union_id_hash bytea NOT NULL UNIQUE,
    status text NOT NULL DEFAULT 'ACTIVE'
        CHECK (status IN ('ACTIVE', 'DISABLED', 'DELETED')),
    last_login_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE iap_orders (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id),
    item_key text NOT NULL,
    huawei_product_id text NOT NULL,
    product_type text NOT NULL
        CHECK (product_type IN ('NONCONSUMABLE', 'AUTORENEWABLE')),
    huawei_order_id text NOT NULL UNIQUE,
    original_order_id text,
    purchase_token_hash bytea NOT NULL,
    purchase_token_ciphertext bytea NOT NULL,
    developer_payload text NOT NULL,
    status text NOT NULL
        CHECK (status IN ('PENDING', 'PURCHASED', 'REFUNDED', 'REVOKED', 'FAILED')),
    purchased_at timestamptz,
    expires_at timestamptz,
    acknowledged_at timestamptz,
    verified_at timestamptz NOT NULL,
    receipt_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX iap_orders_user_id_idx ON iap_orders(user_id);
CREATE INDEX iap_orders_purchase_token_hash_idx ON iap_orders(purchase_token_hash);
CREATE INDEX iap_orders_original_order_id_idx ON iap_orders(original_order_id)
    WHERE original_order_id IS NOT NULL;

CREATE TABLE iap_subscriptions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id),
    item_key text NOT NULL,
    huawei_product_id text NOT NULL,
    subscription_key text NOT NULL UNIQUE,
    latest_order_id uuid REFERENCES iap_orders(id),
    purchase_token_hash bytea NOT NULL,
    purchase_token_ciphertext bytea NOT NULL,
    status text NOT NULL
        CHECK (status IN ('ACTIVE', 'CANCELED_ACTIVE', 'GRACE', 'EXPIRED', 'REFUNDED', 'REVOKED')),
    auto_renewing boolean NOT NULL DEFAULT false,
    starts_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    verified_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (expires_at > starts_at)
);

CREATE INDEX iap_subscriptions_user_id_idx ON iap_subscriptions(user_id);
CREATE INDEX iap_subscriptions_purchase_token_hash_idx ON iap_subscriptions(purchase_token_hash);

CREATE TABLE entitlement_grants (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id),
    entitlement_key text NOT NULL,
    source_type text NOT NULL
        CHECK (source_type IN ('IAP_NONCONSUMABLE', 'IAP_SUBSCRIPTION', 'REDEMPTION', 'ADMIN')),
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

CREATE INDEX entitlement_grants_lookup_idx
    ON entitlement_grants(user_id, entitlement_key, starts_at, ends_at);
CREATE INDEX entitlement_grants_active_idx
    ON entitlement_grants(user_id, entitlement_key)
    WHERE revoked_at IS NULL;

CREATE TABLE redemptions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id),
    campaign_key text NOT NULL,
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

CREATE INDEX redemptions_campaign_user_idx ON redemptions(campaign_key, user_id);
CREATE INDEX redemptions_chain_idx ON redemptions(chain_id, month_ordinal);

CREATE TABLE redemption_campaign_usage (
    campaign_key text PRIMARY KEY,
    redeemed_count bigint NOT NULL DEFAULT 0 CHECK (redeemed_count >= 0),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE redemption_account_usage (
    campaign_key text NOT NULL,
    user_id uuid NOT NULL REFERENCES users(id),
    redeemed_count integer NOT NULL DEFAULT 0 CHECK (redeemed_count >= 0),
    last_redeemed_at timestamptz,
    PRIMARY KEY (campaign_key, user_id)
);

CREATE TABLE iap_webhook_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    huawei_event_id text NOT NULL UNIQUE,
    environment text NOT NULL CHECK (environment IN ('prod', 'test')),
    event_type text NOT NULL,
    signature_valid boolean NOT NULL,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    status text NOT NULL
        CHECK (status IN ('RECEIVED', 'PROCESSING', 'PROCESSED', 'FAILED')),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    last_error text,
    received_at timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz
);

CREATE INDEX iap_webhook_events_status_idx ON iap_webhook_events(status, received_at);

CREATE TABLE entitlement_audit_logs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id),
    entitlement_key text NOT NULL,
    event_type text NOT NULL
        CHECK (event_type IN ('GRANTED', 'EXTENDED', 'EXPIRED', 'REFUNDED', 'REVOKED', 'RESTORED')),
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
