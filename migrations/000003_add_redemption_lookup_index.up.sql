-- Speeds up active gift-chain lookup without introducing a campaign-wide lock.
CREATE INDEX redemptions_user_grant_end_idx
    ON redemptions(user_id, grant_ends_at DESC);
