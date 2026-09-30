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
