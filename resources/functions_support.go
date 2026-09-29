// Package resources is blank-imported by main.go and pkg/commands/list to register every OCI
// resource type with libnuke's registry.
//
// functions_support.go declares listApplicationIDs, the shared helper Function's nested
// enumeration needs: functions.ListFunctionsRequest has NO CompartmentId field at all (verified
// this session, functions/list_functions_request_response.go, ApplicationId mandatory:"true" is
// the ONLY field) -- a Function can only be discovered by walking every Application in the
// compartment first, then paginating ListFunctions per ApplicationId. Mirrors
// resources/file_storage_support.go's listAvailabilityDomainNames shape (paginate, collect a
// scalar identifier, skip anything that can't be used to scope the next call), one level simpler:
// no client-embedding struct is needed here since Application and Function share ONE
// functions.FunctionsManagementClient (pkg/clients.Cache.FunctionsManagement), unlike
// file_storage_support.go's combinedFileStorageClient, which bridges two different SDK clients.
package resources

import (
	"context"
	"fmt"

	"github.com/oracle/oci-go-sdk/v65/functions"
)

// applicationIDLister is the narrow slice of functions.FunctionsManagementClient
// listApplicationIDs needs -- a hand-written interface so this helper is stub-testable with zero
// network access. Named applicationIDLister (not applicationLister) deliberately, to avoid
// colliding with resources/application.go's own applicationLister struct -- every resource file
// in this codebase names its registry.Lister implementation "<type>Lister" (instanceLister,
// exportLister, ...), and Application is no exception; this helper's interface needed a distinct
// name to coexist with that convention in the same package.
type applicationIDLister interface {
	ListApplications(
		ctx context.Context, req functions.ListApplicationsRequest,
	) (functions.ListApplicationsResponse, error)
}

// listApplicationIDs fully paginates functions.ListApplications scoped to compartmentID and
// returns every non-nil Application.Id. ApplicationSummary.Id is mandatory:"true" (verified this
// session, functions/application_summary.go), so in practice every returned item has one -- but
// this helper still nil-checks defensively rather than trusting the tag, and skips (never appends
// an empty string for) any item whose Id somehow comes back nil: an Application with no Id cannot
// scope a ListFunctions call, so it must be excluded from the walk entirely, not silently
// included as "".
func listApplicationIDs(
	ctx context.Context, client applicationIDLister, compartmentID string,
) ([]string, error) {
	var ids []string
	req := functions.ListApplicationsRequest{CompartmentId: &compartmentID}
	for {
		resp, err := client.ListApplications(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("listing Application (for Function enumeration) in %s: %w", compartmentID, err)
		}

		for i := range resp.Items {
			if resp.Items[i].Id == nil {
				continue
			}
			ids = append(ids, *resp.Items[i].Id)
		}

		if resp.OpcNextPage == nil {
			break
		}
		req.Page = resp.OpcNextPage
	}
	return ids, nil
}
