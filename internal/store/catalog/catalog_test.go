package catalog

import (
	"testing"
	"time"
)

func TestCatalogIsValid(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestAnonymousBootstrapOnlyUnlocksDefaults(t *testing.T) {
	now := time.Date(2026, time.September, 30, 8, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	bootstrap := AnonymousBootstrap(now)

	if bootstrap.ServerTime.Location() != time.UTC {
		t.Fatalf("server time location = %v, want UTC", bootstrap.ServerTime.Location())
	}
	if bootstrap.Pass.Active || bootstrap.Redemption.Available {
		t.Fatal("anonymous bootstrap unexpectedly enables pass or redemption")
	}

	allowed := make(map[string]bool)
	for _, item := range bootstrap.Items {
		allowed[item.ItemKey] = item.Access.Allowed
	}
	if !allowed["scene.sunset_coast"] {
		t.Fatalf("default items are not available: %+v", allowed)
	}
	for itemKey, itemAllowed := range allowed {
		if itemAllowed && itemKey != "scene.sunset_coast" {
			t.Fatalf("paid item %q is available anonymously", itemKey)
		}
	}
}

func TestTodayUnlocksLilRollForAnonymousUsers(t *testing.T) {
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	bootstrap := AnonymousBootstrap(now)
	if !bootstrap.Redemption.Available {
		t.Fatal("today's redemption campaign is not available")
	}
	for _, item := range bootstrap.Items {
		if item.ItemKey != "character.lilroll" {
			continue
		}
		if !item.Access.Allowed || item.Access.Reason != AccessLimitedFree || item.FreeWindow == nil {
			t.Fatalf("lilroll snapshot = %+v", item)
		}
		return
	}
	t.Fatal("lilroll is missing from catalog")
}

func TestRedemptionPassBlocksIAPPurchase(t *testing.T) {
	now := time.Date(2026, time.October, 1, 8, 0, 0, 0, time.UTC)
	expiresAt := now.AddDate(0, 1, 0)
	bootstrap := AuthenticatedBootstrap(now, nil, PassSnapshot{
		Active: true, Source: AccessRedemption, ExpiresAt: &expiresAt,
	}, ShareRewardSnapshot{})
	if bootstrap.Pass.IAPPurchaseAllowed || bootstrap.Pass.IAPPurchaseBlockedReason != IAPPurchaseBlockedGiftActive {
		t.Fatalf("pass snapshot = %+v", bootstrap.Pass)
	}
}

func TestPermanentPurchaseReasonWinsOverActivePass(t *testing.T) {
	now := time.Date(2026, time.October, 2, 8, 0, 0, 0, time.UTC)
	expiresAt := now.AddDate(0, 1, 0)
	bootstrap := AuthenticatedBootstrap(now, map[string]Access{
		"character.cloud": {Allowed: true, Reason: AccessIAPPurchase},
	}, PassSnapshot{Active: true, Source: AccessIAPPass, ExpiresAt: &expiresAt}, ShareRewardSnapshot{})

	for _, item := range bootstrap.Items {
		if item.ItemKey != "character.cloud" {
			continue
		}
		if item.Access.Reason != AccessIAPPurchase {
			t.Fatalf("cloud access reason = %q, want %q", item.Access.Reason, AccessIAPPurchase)
		}
		return
	}
	t.Fatal("cloud is missing from catalog")
}

func TestActivePassUnlocksEveryPaidCharacterAndScene(t *testing.T) {
	now := time.Date(2026, time.October, 3, 8, 0, 0, 0, time.UTC)
	expiresAt := now.AddDate(0, 1, 0)
	bootstrap := AuthenticatedBootstrap(now, nil, PassSnapshot{
		Active: true, Source: AccessIAPPass, ExpiresAt: &expiresAt,
	}, ShareRewardSnapshot{})

	checked := 0
	for _, item := range bootstrap.Items {
		if item.HuaweiProductID == "" || (item.Kind != KindCharacter && item.Kind != KindScene) {
			continue
		}
		checked++
		if !item.IncludedInPass || !item.Access.Allowed || item.Access.Reason != AccessIAPPass {
			t.Errorf("paid item %q is not unlocked by pass: %+v", item.ItemKey, item)
		}
	}
	if checked == 0 {
		t.Fatal("catalog has no paid characters or scenes")
	}
}

// November 2026 promotion: lamp and lilroll are free from 2026-11-03 00:00
// through the end of 2026-11-30, Beijing time.
func TestNovemberLimitedFreeWindowBoundaries(t *testing.T) {
	beijing := time.FixedZone("CST", 8*60*60)
	cases := []struct {
		at   time.Time
		free bool
	}{
		{time.Date(2026, time.November, 2, 23, 59, 59, 0, beijing), false},
		{time.Date(2026, time.November, 3, 0, 0, 0, 0, beijing), true},
		{time.Date(2026, time.November, 30, 23, 59, 59, 0, beijing), true},
		{time.Date(2026, time.December, 1, 0, 0, 0, 0, beijing), false},
	}
	for _, tc := range cases {
		access := map[string]string{}
		for _, item := range AnonymousBootstrap(tc.at).Items {
			access[item.ItemKey] = item.Access.Reason
		}
		for _, key := range []string{"character.lamp", "character.lilroll"} {
			if (access[key] == AccessLimitedFree) != tc.free {
				t.Fatalf("%s at %s: reason %q, want free=%v", key, tc.at, access[key], tc.free)
			}
		}
		if access["character.cloud"] != AccessLocked {
			t.Fatalf("cloud must stay locked at %s, got %q", tc.at, access["character.cloud"])
		}
	}
}

// campaign.zero_width_20261001 stays open through the end of 2026-12-31, Beijing time.
func TestZeroWidthCampaignEndsAfterDecember31(t *testing.T) {
	beijing := time.FixedZone("CST", 8*60*60)
	active := func(at time.Time) bool {
		_, ok := FindActiveCampaignByCodeKey("code.zero_width_20261001", at)
		return ok
	}
	if !active(time.Date(2026, time.November, 3, 12, 0, 0, 0, beijing)) ||
		!active(time.Date(2026, time.December, 31, 23, 59, 59, 0, beijing)) {
		t.Fatal("campaign must be open through 2026-12-31 Beijing time")
	}
	if active(time.Date(2027, time.January, 1, 0, 0, 0, 0, beijing)) {
		t.Fatal("campaign must close at 2027-01-01 00:00 Beijing time")
	}
	campaign, _ := FindCampaignByCodeKey("code.zero_width_20261001")
	if campaign.Rule != RedemptionRuleOncePerAccount || campaign.PerAccountLimit != 1 {
		t.Fatalf("extension must keep the once-per-account rule: %+v", campaign)
	}
}
