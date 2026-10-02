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
	if !allowed["character.matchman"] || !allowed["scene.sunset_coast"] {
		t.Fatalf("default items are not available: %+v", allowed)
	}
	for itemKey, itemAllowed := range allowed {
		if itemAllowed && itemKey != "character.matchman" && itemKey != "scene.sunset_coast" {
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
	bootstrap := AuthenticatedBootstrap(now, "binding", nil, PassSnapshot{
		Active: true, Source: AccessRedemption, ExpiresAt: &expiresAt,
	})
	if bootstrap.Pass.IAPPurchaseAllowed || bootstrap.Pass.IAPPurchaseBlockedReason != IAPPurchaseBlockedGiftActive {
		t.Fatalf("pass snapshot = %+v", bootstrap.Pass)
	}
}

func TestPermanentPurchaseReasonWinsOverActivePass(t *testing.T) {
	now := time.Date(2026, time.October, 2, 8, 0, 0, 0, time.UTC)
	expiresAt := now.AddDate(0, 1, 0)
	bootstrap := AuthenticatedBootstrap(now, "binding", map[string]Access{
		"character.cloud": {Allowed: true, Reason: AccessIAPPurchase},
	}, PassSnapshot{Active: true, Source: AccessIAPPass, ExpiresAt: &expiresAt})

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
