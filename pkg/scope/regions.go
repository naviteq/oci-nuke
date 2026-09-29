package scope

import (
	"context"
	"fmt"
	"strings"

	"github.com/oracle/oci-go-sdk/v65/identity"
)

// RegionClient is the narrow slice of identity.IdentityClient ResolveRegions needs -- a
// hand-written interface so tests can fake it with zero network, mirroring IdentityClient's
// shape in tree.go.
type RegionClient interface {
	ListRegionSubscriptions(ctx context.Context, req identity.ListRegionSubscriptionsRequest) (identity.ListRegionSubscriptionsResponse, error)
}

// ResolveRegions issues the ONE Identity API call this function needs (ListRegionSubscriptions
// against tenancyOCID), and uses it to answer two questions at once: (1) which of configRegions
// are actually valid, live, subscribed regions in this tenancy, and (2) what the tenancy's real
// home region is.
//
// homeRegion is taken EXCLUSIVELY from the subscription entry with IsHomeRegion=true --
// never from a common.ConfigurationProvider's Region() (that value is intentionally not even a
// parameter here). provider.Region() is the credential profile's *connection* region, which can
// diverge from the tenancy's actual home region -- live-verified against a real tenancy, where the
// NAVITEQ-OPN profile connects via eu-frankfurt-1 but the tenancy's IsHomeRegion=true entry is
// us-ashburn-1. Every call site that needs the home region MUST source it from this function's
// return value, not the connection region.
//
// Each entry of configRegions is validated in order (so validated's order matches the operator's
// own declaration order, not the API response's order) against the subscribed set: absent from
// the subscribed regions ("unknown") or present but not RegionSubscriptionStatusReady
// ("unsubscribed") both fail validation loudly, before any scanner is built. On any validation
// failure this function returns immediately with (nil, "", err) -- homeRegion never leaks a
// partially-resolved value on the error path.
func ResolveRegions(
	ctx context.Context,
	client RegionClient,
	tenancyOCID string,
	configRegions []string,
) (validated []string, homeRegion string, err error) {
	resp, err := client.ListRegionSubscriptions(ctx, identity.ListRegionSubscriptionsRequest{
		TenancyId: &tenancyOCID,
	})
	if err != nil {
		return nil, "", fmt.Errorf("listing region subscriptions: %w", err)
	}

	subscribed := map[string]identity.RegionSubscription{}
	homeRegionCount := 0
	for _, rs := range resp.Items {
		if rs.RegionName == nil {
			continue
		}
		subscribed[strings.ToLower(*rs.RegionName)] = rs
		if rs.IsHomeRegion != nil && *rs.IsHomeRegion {
			homeRegionCount++
			homeRegion = *rs.RegionName
		}
	}
	switch {
	case homeRegionCount == 0:
		return nil, "", fmt.Errorf("no home region (IsHomeRegion=true) found among %d subscribed regions", len(resp.Items))
	case homeRegionCount > 1:
		return nil, "", fmt.Errorf("expected exactly one home region, found %d among %d subscribed regions", homeRegionCount, len(resp.Items))
	}

	for _, r := range configRegions {
		rs, ok := subscribed[strings.ToLower(r)]
		if !ok {
			return nil, "", fmt.Errorf("region %q is not a subscribed region in this tenancy", r)
		}
		if rs.Status != identity.RegionSubscriptionStatusReady {
			return nil, "", fmt.Errorf("region %q is subscribed but not ready (status=%s)", r, rs.Status)
		}
		validated = append(validated, r)
	}
	return validated, homeRegion, nil
}
