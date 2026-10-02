-- Restore only needs purchased non-consumables; renewals must not enlarge this index.
CREATE INDEX iap_provider_transactions_nonconsumable_user_idx
    ON iap_provider_transactions(user_id, huawei_product_id, purchase_order_id)
    WHERE trade_type = 'PURCHASE' AND product_type = 'NONCONSUMABLE';

-- Replace the older active-grant index rather than retaining two overlapping
-- indexes. No query needs entitlement_key ordering, while Bootstrap needs time bounds.
CREATE INDEX entitlement_grants_active_window_idx
    ON entitlement_grants(user_id, ends_at, starts_at)
    WHERE revoked_at IS NULL;
DROP INDEX entitlement_grants_active_idx;

-- Global redemption limits read the redemptions fact table directly. Keeping an
-- unwritten counter table would imply consistency guarantees the service does not provide.
DROP TABLE redemption_campaign_usage;
