CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    huawei_union_id_hash bytea NOT NULL UNIQUE,
    developer_payload text UNIQUE,
    status text NOT NULL DEFAULT 'ACTIVE'
        CHECK (status IN ('ACTIVE', 'DISABLED', 'DELETED')),
    last_login_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE iap_subscriptions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider text NOT NULL DEFAULT 'HUAWEI' CHECK (provider = 'HUAWEI'),
    environment text NOT NULL CHECK (environment IN ('SANDBOX', 'PRODUCTION')),
    user_id uuid NOT NULL REFERENCES users(id),
    item_key text NOT NULL,
    huawei_product_id text NOT NULL,
    subscription_key text NOT NULL,
    latest_period_id uuid,
    latest_purchase_order_id text,
    developer_payload text NOT NULL,
    status text NOT NULL
        CHECK (status IN ('ACTIVE', 'CANCELED_ACTIVE', 'EXPIRED', 'REVOKED')),
    auto_renewing boolean NOT NULL DEFAULT false,
    starts_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    verified_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, environment, user_id, huawei_product_id, subscription_key),
    CHECK (expires_at > starts_at),
    CHECK (revoked_at IS NOT NULL OR status <> 'REVOKED')
);

CREATE INDEX iap_subscriptions_user_status_idx
    ON iap_subscriptions(user_id, status, expires_at);
CREATE INDEX iap_subscriptions_product_idx
    ON iap_subscriptions(provider, environment, huawei_product_id);
CREATE INDEX iap_subscriptions_developer_payload_idx
    ON iap_subscriptions(provider, environment, developer_payload);

CREATE TABLE iap_subscription_tokens (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider text NOT NULL DEFAULT 'HUAWEI' CHECK (provider = 'HUAWEI'),
    environment text NOT NULL CHECK (environment IN ('SANDBOX', 'PRODUCTION')),
    subscription_id uuid NOT NULL REFERENCES iap_subscriptions(id) ON DELETE CASCADE,
    purchase_token_hash bytea NOT NULL,
    purchase_token_ciphertext bytea NOT NULL,
    first_seen_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    source text NOT NULL CHECK (source IN ('WEBHOOK', 'CLIENT_RESTORE', 'RECONCILIATION')),
    UNIQUE (provider, environment, purchase_token_hash)
);

CREATE INDEX iap_subscription_tokens_subscription_seen_idx
    ON iap_subscription_tokens(subscription_id, last_seen_at DESC);

CREATE TABLE iap_provider_transactions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider text NOT NULL DEFAULT 'HUAWEI' CHECK (provider = 'HUAWEI'),
    environment text NOT NULL CHECK (environment IN ('SANDBOX', 'PRODUCTION')),
    user_id uuid NOT NULL REFERENCES users(id),
    subscription_id uuid REFERENCES iap_subscriptions(id),
    item_key text NOT NULL,
    huawei_product_id text NOT NULL,
    product_type text NOT NULL CHECK (product_type IN ('NONCONSUMABLE', 'AUTORENEWABLE')),
    purchase_order_id text NOT NULL,
    original_purchase_order_id text,
    purchase_token_hash bytea NOT NULL,
    purchase_token_ciphertext bytea NOT NULL,
    developer_payload text NOT NULL,
    trade_type text NOT NULL CHECK (trade_type IN ('PURCHASE', 'REFUND')),
    transaction_subtype text NOT NULL CHECK (transaction_subtype IN ('INITIAL', 'RENEWAL', 'REFUND')),
    refund_type text CHECK (refund_type IN ('WITHDRAWAL', 'RETURN_FEE', 'USER_REFUND', 'UNKNOWN')),
    entitlement_effect text NOT NULL CHECK (entitlement_effect IN ('NONE', 'KEEP', 'REVOKE', 'PENDING')),
    related_purchase_transaction_id uuid REFERENCES iap_provider_transactions(id),
    provider_status text NOT NULL,
    amount numeric,
    currency text,
    occurred_at timestamptz NOT NULL,
    acknowledged_at timestamptz,
    verified_at timestamptz NOT NULL,
    payload_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, environment, purchase_order_id, trade_type),
    CHECK ((trade_type = 'PURCHASE' AND refund_type IS NULL AND transaction_subtype IN ('INITIAL', 'RENEWAL'))
        OR (trade_type = 'REFUND' AND refund_type IS NOT NULL AND transaction_subtype = 'REFUND')),
    CHECK (trade_type <> 'REFUND' OR entitlement_effect IN ('KEEP', 'REVOKE', 'PENDING'))
);

CREATE INDEX iap_provider_transactions_user_idx ON iap_provider_transactions(user_id, verified_at DESC);
CREATE INDEX iap_provider_transactions_token_idx
    ON iap_provider_transactions(provider, environment, purchase_token_hash);
CREATE INDEX iap_provider_transactions_subscription_idx
    ON iap_provider_transactions(subscription_id, occurred_at DESC);
CREATE INDEX iap_provider_transactions_related_idx
    ON iap_provider_transactions(related_purchase_transaction_id)
    WHERE related_purchase_transaction_id IS NOT NULL;
CREATE INDEX iap_provider_transactions_developer_payload_idx
    ON iap_provider_transactions(provider, environment, developer_payload);

CREATE TABLE iap_subscription_periods (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider text NOT NULL DEFAULT 'HUAWEI' CHECK (provider = 'HUAWEI'),
    environment text NOT NULL CHECK (environment IN ('SANDBOX', 'PRODUCTION')),
    subscription_id uuid NOT NULL REFERENCES iap_subscriptions(id) ON DELETE CASCADE,
    purchase_transaction_id uuid NOT NULL REFERENCES iap_provider_transactions(id),
    purchase_token_id uuid NOT NULL REFERENCES iap_subscription_tokens(id),
    purchase_order_id text NOT NULL,
    starts_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    status text NOT NULL CHECK (status IN ('PAID', 'EXPIRED', 'REVOKED')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, environment, purchase_order_id),
    UNIQUE (purchase_transaction_id),
    CHECK (expires_at > starts_at)
);

ALTER TABLE iap_subscriptions
    ADD CONSTRAINT iap_subscriptions_latest_period_fk
    FOREIGN KEY (latest_period_id) REFERENCES iap_subscription_periods(id);

CREATE INDEX iap_subscription_periods_subscription_idx
    ON iap_subscription_periods(subscription_id, expires_at DESC);

CREATE TABLE iap_refund_requests (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider text NOT NULL DEFAULT 'HUAWEI' CHECK (provider = 'HUAWEI'),
    environment text NOT NULL CHECK (environment IN ('SANDBOX', 'PRODUCTION')),
    subscription_id uuid NOT NULL REFERENCES iap_subscriptions(id),
    purchase_transaction_id uuid NOT NULL REFERENCES iap_provider_transactions(id),
    purchase_token_hash bytea NOT NULL,
    operation_type text NOT NULL CHECK (operation_type IN ('RETURN_FEE', 'WITHDRAWAL')),
    status text NOT NULL CHECK (status IN ('PENDING', 'SUCCEEDED', 'FAILED')),
    requested_at timestamptz NOT NULL,
    completed_at timestamptz,
    response_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX iap_refund_requests_match_idx
    ON iap_refund_requests(provider, environment, purchase_token_hash, status);

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
    provider text NOT NULL DEFAULT 'HUAWEI' CHECK (provider = 'HUAWEI'),
    environment text NOT NULL CHECK (environment IN ('SANDBOX', 'PRODUCTION')),
    huawei_event_id text NOT NULL,
    notification_type text NOT NULL,
    notification_subtype text NOT NULL DEFAULT '',
    signature_valid boolean NOT NULL,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    status text NOT NULL CHECK (status IN ('RECEIVED', 'PROCESSING', 'PROCESSED', 'FAILED')),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    last_error text,
    received_at timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz,
    UNIQUE (provider, environment, huawei_event_id)
);

CREATE INDEX iap_webhook_events_status_idx ON iap_webhook_events(status, received_at);

CREATE TABLE iap_reconciliation_checkpoints (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider text NOT NULL DEFAULT 'HUAWEI' CHECK (provider = 'HUAWEI'),
    environment text NOT NULL CHECK (environment IN ('SANDBOX', 'PRODUCTION')),
    job_name text NOT NULL CHECK (job_name IN (
        'trade_reconciliation', 'trade_backfill',
        'trade_reconciliation_observe', 'trade_backfill_observe'
    )),
    direction text NOT NULL CHECK (direction IN ('FORWARD', 'BACKWARD')),
    window_start timestamptz,
    window_end timestamptz,
    continuation_token text,
    page_number integer NOT NULL DEFAULT 0 CHECK (page_number >= 0),
    status text NOT NULL CHECK (status IN ('IDLE', 'RUNNING', 'COMPLETED', 'FAILED', 'RESET_REQUIRED')),
    last_success_at timestamptz,
    last_error text,
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, environment, job_name),
    CHECK (window_start IS NULL OR window_end IS NULL OR window_end > window_start)
);

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
