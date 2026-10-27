package catalog

import "time"

// ShareRewardSnapshot exposes public share-reward campaign state on bootstrap.
// Account-specific grant details are filled by the entitlement service when wired.
type ShareRewardSnapshot struct {
	Available bool `json:"available"`
}

// AnonymousShareRewardSnapshot returns catalog-visible share-reward metadata for
// unauthenticated clients. Campaigns are versioned here when added.
func AnonymousShareRewardSnapshot(_ time.Time) ShareRewardSnapshot {
	return ShareRewardSnapshot{Available: false}
}
