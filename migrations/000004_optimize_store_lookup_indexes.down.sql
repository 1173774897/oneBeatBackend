CREATE TABLE redemption_campaign_usage (
    campaign_key text PRIMARY KEY,
    redeemed_count bigint NOT NULL DEFAULT 0 CHECK (redeemed_count >= 0),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX entitlement_grants_active_idx
    ON entitlement_grants(user_id, entitlement_key)
    WHERE revoked_at IS NULL;
DROP INDEX IF EXISTS entitlement_grants_active_window_idx;
DROP INDEX IF EXISTS iap_provider_transactions_nonconsumable_user_idx;
