package clients

import (
	"net/http"

	"golang.org/x/sync/semaphore"

	"github.com/oracle/oci-go-sdk/v65/common"
)

// DefaultMaxConcurrentRequests bounds the total number of in-flight HTTP requests across every
// OCI SDK client this package's Cache constructs, tenant-wide. It exists to close the gap
// common.DefaultRetryPolicy() does not cover: the retry policy absorbs transient per-call
// throttling, but nothing bounds TOTAL concurrent OCI API calls once Phase 3 fans scanners out
// across regions -- scanner.DefaultParallelQueries=16 per scanner, multiplied by region count on
// top of compartment count, with no cross-scanner throttle otherwise (03-RESEARCH.md Q7).
//
// This is a package variable rather than a New parameter so no existing pkg/commands/run call
// site needs to change this phase, and it is intentionally NOT tuned against real call volume --
// there is nothing real to tune against yet with zero resource types registered
// (03-RESEARCH.md Assumption A4). Sized conservatively; revisit once Phase 4/5 land real
// scanners.
var DefaultMaxConcurrentRequests = 32

// boundedDispatcher wraps an inner common.HTTPRequestDispatcher, gating every Do call through a
// shared, tenant-wide semaphore so no single client -- nor the sum of every client this Cache
// constructs -- can issue unbounded concurrent HTTP requests.
type boundedDispatcher struct {
	inner common.HTTPRequestDispatcher
	sem   *semaphore.Weighted
}

// Do acquires one unit from sem using req's own context (so a request whose context is already
// canceled does not block acquiring -- it fails fast instead), then delegates to inner,
// releasing the acquired unit via defer regardless of the inner call's outcome.
func (d *boundedDispatcher) Do(req *http.Request) (*http.Response, error) {
	if err := d.sem.Acquire(req.Context(), 1); err != nil {
		return nil, err
	}
	defer d.sem.Release(1)
	return d.inner.Do(req)
}
