package catalog

import (
	"fmt"
	"time"
)

const ConfigVersion = "2026-10-02.1"

const (
	KindPass      = "PASS"
	KindCharacter = "CHARACTER"
	KindScene     = "SCENE"
	KindFeature   = "FEATURE"

	ProductNonConsumable = "NONCONSUMABLE"
	ProductAutoRenewable = "AUTORENEWABLE"

	AccessDefaultFree = "DEFAULT_FREE"
	AccessLocked      = "LOCKED"
	AccessIAPPurchase = "IAP_PURCHASE"
	AccessIAPPass     = "PASS_IAP"
	AccessRedemption  = "PASS_REDEMPTION"
	AccessLimitedFree = "LIMITED_FREE"

	IAPPurchaseBlockedGiftActive = "ACTIVE_REDEMPTION_PASS"
	IAPPurchaseBlockedPassActive = "ACTIVE_IAP_PASS"
)

// Item is the public, versioned configuration for one sellable or gated item.
// Prices intentionally do not live here; clients must obtain localized prices from Huawei IAP.
type Item struct {
	ItemKey          string `json:"itemKey"`
	DisplayName      string `json:"displayName"`
	Kind             string `json:"kind"`
	HuaweiProductID  string `json:"huaweiProductId,omitempty"`
	IAPProductType   string `json:"iapProductType,omitempty"`
	DefaultFree      bool   `json:"defaultFree"`
	IncludedInPass   bool   `json:"includedInPass"`
	RepeatableChoice bool   `json:"repeatableChoice,omitempty"`
}

type Access struct {
	Allowed    bool       `json:"allowed"`
	Reason     string     `json:"reason"`
	ValidUntil *time.Time `json:"validUntil"`
}

type FreeWindow struct {
	StartsAt time.Time `json:"startsAt"`
	EndsAt   time.Time `json:"endsAt"`
}

type ItemSnapshot struct {
	Item
	Access     Access      `json:"access"`
	FreeWindow *FreeWindow `json:"freeWindow"`
}

type PassSnapshot struct {
	Active                   bool       `json:"active"`
	Source                   string     `json:"source,omitempty"`
	Status                   string     `json:"status,omitempty"`
	ExpiresAt                *time.Time `json:"expiresAt"`
	AutoRenewing             bool       `json:"autoRenewing"`
	IAPPurchaseAllowed       bool       `json:"iapPurchaseAllowed"`
	IAPPurchaseBlockedReason string     `json:"iapPurchaseBlockedReason,omitempty"`
}

type RedemptionSnapshot struct {
	Available bool `json:"available"`
}

type Bootstrap struct {
	ServerTime       time.Time          `json:"serverTime"`
	ConfigVersion    string             `json:"configVersion"`
	DeveloperPayload string             `json:"developerPayload,omitempty"`
	Pass             PassSnapshot       `json:"pass"`
	Items            []ItemSnapshot     `json:"items"`
	Redemption       RedemptionSnapshot `json:"redemption"`
}

var items = []Item{
	{ItemKey: "pass.all", DisplayName: "畅游月卡", Kind: KindPass, HuaweiProductID: "onebeat.pass.monthly", IAPProductType: ProductAutoRenewable, IncludedInPass: true},
	{ItemKey: "character.matchman", DisplayName: "火柴人", Kind: KindCharacter, DefaultFree: true, IncludedInPass: true, RepeatableChoice: true},
	{ItemKey: "character.cloud", DisplayName: "云行者", Kind: KindCharacter, HuaweiProductID: "onebeat.character.cloud", IAPProductType: ProductNonConsumable, IncludedInPass: true},
	{ItemKey: "character.teapot", DisplayName: "茶壶太太", Kind: KindCharacter, HuaweiProductID: "onebeat.character.teapot", IAPProductType: ProductNonConsumable, IncludedInPass: true},
	{ItemKey: "character.wave", DisplayName: "Wave", Kind: KindCharacter, HuaweiProductID: "onebeat.character.wave", IAPProductType: ProductNonConsumable, IncludedInPass: true},
	{ItemKey: "character.mismatch", DisplayName: "条纹步客", Kind: KindCharacter, HuaweiProductID: "onebeat.character.mismatch", IAPProductType: ProductNonConsumable, IncludedInPass: true},
	{ItemKey: "character.scooter", DisplayName: "思古特", Kind: KindCharacter, HuaweiProductID: "onebeat.character.scooter", IAPProductType: ProductNonConsumable, IncludedInPass: true},
	{ItemKey: "character.lilroll", DisplayName: "轮滑小子", Kind: KindCharacter, HuaweiProductID: "onebeat.character.lilroll", IAPProductType: ProductNonConsumable, IncludedInPass: true},
	{ItemKey: "scene.sunset_coast", DisplayName: "夕阳海边", Kind: KindScene, DefaultFree: true, IncludedInPass: true},
	{ItemKey: "scene.neon_street", DisplayName: "霓虹街口", Kind: KindScene, HuaweiProductID: "onebeat.scene.neon_street", IAPProductType: ProductNonConsumable, IncludedInPass: true},
	{ItemKey: "feature.pro_drum_machine", DisplayName: "PRO 定制鼓机", Kind: KindFeature, IncludedInPass: true},
}

// Items returns a copy so callers cannot mutate the process-wide catalog.
func Items() []Item {
	result := make([]Item, len(items))
	copy(result, items)
	return result
}

func FindByProductID(productID string) (Item, bool) {
	for _, item := range items {
		if item.HuaweiProductID == productID {
			return item, true
		}
	}
	return Item{}, false
}

func FindByItemKey(itemKey string) (Item, bool) {
	for _, item := range items {
		if item.ItemKey == itemKey {
			return item, true
		}
	}
	return Item{}, false
}

// AnonymousBootstrap exposes only public configuration and default-free access.
// Account-scoped grants are added by the authenticated entitlement service later.
func AnonymousBootstrap(now time.Time) Bootstrap {
	return Bootstrap{
		ServerTime:    now.UTC(),
		ConfigVersion: ConfigVersion,
		Pass:          finalizePass(PassSnapshot{}),
		Items:         itemSnapshots(now, nil, PassSnapshot{}),
		Redemption:    RedemptionSnapshot{Available: len(ActiveRedemptionCampaigns(now)) > 0},
	}
}

// AuthenticatedBootstrap overlays verified account grants on the public catalog.
func AuthenticatedBootstrap(
	now time.Time,
	developerPayload string,
	grants map[string]Access,
	pass PassSnapshot,
) Bootstrap {
	pass = finalizePass(pass)
	return Bootstrap{
		ServerTime:       now.UTC(),
		ConfigVersion:    ConfigVersion,
		DeveloperPayload: developerPayload,
		Pass:             pass,
		Items:            itemSnapshots(now, grants, pass),
		Redemption:       RedemptionSnapshot{Available: len(ActiveRedemptionCampaigns(now)) > 0},
	}
}

func itemSnapshots(now time.Time, grants map[string]Access, pass PassSnapshot) []ItemSnapshot {
	snapshots := make([]ItemSnapshot, 0, len(items))
	for _, item := range items {
		access := Access{Allowed: false, Reason: AccessLocked}
		var freeWindow *FreeWindow
		// Keep access precedence stable: permanent/default access and passes must
		// not be relabeled as a shorter-lived promotion.
		if item.DefaultFree {
			access = Access{Allowed: true, Reason: AccessDefaultFree}
		} else if pass.Active && item.IncludedInPass {
			access = Access{Allowed: true, Reason: pass.Source, ValidUntil: pass.ExpiresAt}
		} else if grant, ok := grants[item.ItemKey]; ok {
			access = grant
		} else if window, ok := CurrentFreeWindow(item.ItemKey, now); ok {
			windowCopy := FreeWindow{StartsAt: window.StartsAt, EndsAt: window.EndsAt}
			freeWindow = &windowCopy
			endsAt := window.EndsAt
			access = Access{Allowed: true, Reason: AccessLimitedFree, ValidUntil: &endsAt}
		}
		snapshots = append(snapshots, ItemSnapshot{Item: item, Access: access, FreeWindow: freeWindow})
	}
	return snapshots
}

func finalizePass(pass PassSnapshot) PassSnapshot {
	// A live pass blocks opening a second subscription in the normal client flow.
	// Paid Huawei orders that already completed are still verified server-side.
	pass.IAPPurchaseAllowed = !pass.Active
	if !pass.Active {
		pass.IAPPurchaseBlockedReason = ""
		return pass
	}
	if pass.Source == AccessRedemption {
		pass.IAPPurchaseBlockedReason = IAPPurchaseBlockedGiftActive
	} else {
		pass.IAPPurchaseBlockedReason = IAPPurchaseBlockedPassActive
	}
	return pass
}

// Validate catches duplicate IDs and malformed IAP mappings during startup and tests.
func Validate() error {
	itemKeys := make(map[string]struct{}, len(items))
	productIDs := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item.ItemKey == "" || item.DisplayName == "" || item.Kind == "" {
			return fmt.Errorf("catalog item has required empty field: %+v", item)
		}
		if _, exists := itemKeys[item.ItemKey]; exists {
			return fmt.Errorf("duplicate item key %q", item.ItemKey)
		}
		itemKeys[item.ItemKey] = struct{}{}
		if item.HuaweiProductID == "" {
			if item.IAPProductType != "" {
				return fmt.Errorf("item %q has product type without product ID", item.ItemKey)
			}
			continue
		}
		if item.IAPProductType != ProductNonConsumable && item.IAPProductType != ProductAutoRenewable {
			return fmt.Errorf("item %q has unsupported product type %q", item.ItemKey, item.IAPProductType)
		}
		if _, exists := productIDs[item.HuaweiProductID]; exists {
			return fmt.Errorf("duplicate Huawei product ID %q", item.HuaweiProductID)
		}
		productIDs[item.HuaweiProductID] = struct{}{}
	}
	return validatePromotions(itemKeys)
}
