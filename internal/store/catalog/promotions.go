package catalog

import (
	"fmt"
	"time"
)

const (
	RedemptionRuleOncePerAccount      = "ONCE_PER_ACCOUNT"
	RedemptionRuleUnlimitedPerAccount = "UNLIMITED_PER_ACCOUNT"
)

// RedemptionCampaign is public, versioned campaign metadata. Secret plaintext
// codes and their HMAC digests deliberately live outside this package.
type RedemptionCampaign struct {
	CampaignKey        string
	CodeKeys           []string
	StartsAt           time.Time
	EndsAt             time.Time
	Rule               string
	PerAccountLimit    int
	GlobalLimit        int64
	GrantCalendarMonth int
}

// LimitedFreeWindow grants temporary access without creating an account grant.
type LimitedFreeWindow struct {
	ItemKey  string
	StartsAt time.Time
	EndsAt   time.Time
}

var redemptionCampaigns = []RedemptionCampaign{
	{
		CampaignKey:        "campaign.zero_width_20261001",
		CodeKeys:           []string{"code.zero_width_20261001"},
		StartsAt:           time.Date(2026, time.September, 30, 16, 0, 0, 0, time.UTC),
		EndsAt:             time.Date(2026, time.October, 10, 16, 0, 0, 0, time.UTC),
		Rule:               RedemptionRuleOncePerAccount,
		PerAccountLimit:    1,
		GrantCalendarMonth: 1,
	},
	{
		CampaignKey:        "campaign.xianluoexclusive",
		CodeKeys:           []string{"code.xianluoexclusive"},
		StartsAt:           time.Date(2026, time.September, 30, 16, 0, 0, 0, time.UTC),
		EndsAt:             time.Date(2050, time.October, 1, 16, 0, 0, 0, time.UTC),
		Rule:               RedemptionRuleUnlimitedPerAccount,
		PerAccountLimit:    0,
		GrantCalendarMonth: 1,
	},
}

var limitedFreeWindows = []LimitedFreeWindow{
	{
		ItemKey:  "character.lilroll",
		StartsAt: time.Date(2026, time.September, 30, 16, 0, 0, 0, time.UTC),
		EndsAt:   time.Date(2026, time.October, 10, 16, 0, 0, 0, time.UTC),
	},
}

// RedemptionCampaigns returns defensive copies of the compiled campaigns.
func RedemptionCampaigns() []RedemptionCampaign {
	result := make([]RedemptionCampaign, len(redemptionCampaigns))
	for index, campaign := range redemptionCampaigns {
		result[index] = campaign
		result[index].CodeKeys = append([]string(nil), campaign.CodeKeys...)
	}
	return result
}

// ActiveRedemptionCampaigns returns only campaigns active in the left-closed,
// right-open interval [StartsAt, EndsAt).
func ActiveRedemptionCampaigns(now time.Time) []RedemptionCampaign {
	result := make([]RedemptionCampaign, 0, len(redemptionCampaigns))
	for _, campaign := range redemptionCampaigns {
		if !now.Before(campaign.StartsAt) && now.Before(campaign.EndsAt) {
			result = append(result, campaign)
		}
	}
	return result
}

// FindActiveCampaignByCodeKey resolves a code only when its campaign is active.
func FindActiveCampaignByCodeKey(codeKey string, now time.Time) (RedemptionCampaign, bool) {
	for _, campaign := range ActiveRedemptionCampaigns(now) {
		for _, candidate := range campaign.CodeKeys {
			if candidate == codeKey {
				return campaign, true
			}
		}
	}
	return RedemptionCampaign{}, false
}

// FindCampaignByCodeKey resolves historical or inactive campaigns as well. The
// service uses this after an idempotency lookup so successful requests remain replayable.
func FindCampaignByCodeKey(codeKey string) (RedemptionCampaign, bool) {
	for _, campaign := range redemptionCampaigns {
		for _, candidate := range campaign.CodeKeys {
			if candidate == codeKey {
				return campaign, true
			}
		}
	}
	return RedemptionCampaign{}, false
}

// RequiredRedemptionCodeKeys lists every private digest required at startup.
func RequiredRedemptionCodeKeys() []string {
	keys := make([]string, 0)
	for _, campaign := range redemptionCampaigns {
		keys = append(keys, campaign.CodeKeys...)
	}
	return keys
}

// CurrentFreeWindow intentionally exposes no future promotion schedule.
func CurrentFreeWindow(itemKey string, now time.Time) (FreeWindow, bool) {
	for _, window := range limitedFreeWindows {
		if window.ItemKey == itemKey && !now.Before(window.StartsAt) && now.Before(window.EndsAt) {
			return FreeWindow{StartsAt: window.StartsAt.UTC(), EndsAt: window.EndsAt.UTC()}, true
		}
	}
	return FreeWindow{}, false
}

func validatePromotions(itemKeys map[string]struct{}) error {
	campaignKeys := make(map[string]struct{}, len(redemptionCampaigns))
	codeKeys := make(map[string]struct{})
	for _, campaign := range redemptionCampaigns {
		if campaign.CampaignKey == "" || len(campaign.CodeKeys) == 0 || !campaign.EndsAt.After(campaign.StartsAt) {
			return fmt.Errorf("invalid redemption campaign %q", campaign.CampaignKey)
		}
		validAccountRule := campaign.Rule == RedemptionRuleOncePerAccount && campaign.PerAccountLimit == 1
		validAccountRule = validAccountRule ||
			campaign.Rule == RedemptionRuleUnlimitedPerAccount && campaign.PerAccountLimit == 0
		if !validAccountRule || campaign.GrantCalendarMonth != 1 {
			return fmt.Errorf("unsupported redemption campaign rule for %q", campaign.CampaignKey)
		}
		if _, exists := campaignKeys[campaign.CampaignKey]; exists {
			return fmt.Errorf("duplicate redemption campaign key %q", campaign.CampaignKey)
		}
		campaignKeys[campaign.CampaignKey] = struct{}{}
		for _, codeKey := range campaign.CodeKeys {
			if codeKey == "" {
				return fmt.Errorf("campaign %q has empty code key", campaign.CampaignKey)
			}
			if _, exists := codeKeys[codeKey]; exists {
				return fmt.Errorf("duplicate redemption code key %q", codeKey)
			}
			codeKeys[codeKey] = struct{}{}
		}
	}
	for _, window := range limitedFreeWindows {
		if _, exists := itemKeys[window.ItemKey]; !exists {
			return fmt.Errorf("limited-free item %q does not exist", window.ItemKey)
		}
		if !window.EndsAt.After(window.StartsAt) {
			return fmt.Errorf("invalid limited-free window for %q", window.ItemKey)
		}
	}
	return nil
}
