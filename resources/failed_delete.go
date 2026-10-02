package resources

import (
	"fmt"
	"net/http"

	liberrors "github.com/ekristen/libnuke/pkg/errors"
	"github.com/oracle/oci-go-sdk/v65/common"
)

// maxFailedDeletes bounds how many deletes of one resource may end in FAILED before Remove()
// stops re-issuing them. Without it an item whose delete OCI keeps accepting and then failing
// never stops: each re-delete moves it back to ItemStateWaiting, which resets libnuke's
// handleFailure counter, and each FAILED round moves it to ItemStateFailed, which resets
// handleWaiting's counter. Neither --max-wait-retries nor the failedCount>=2 abort ever fires.
const maxFailedDeletes = 3

// deleteOutcome is what one post-delete GET says about a resource.
type deleteOutcome int

const (
	// deleteNotStarted: the resource is still in a live state (ACTIVE, UPDATING, ...). The
	// delete either has not landed yet or was refused with a 409 and is being held.
	deleteNotStarted deleteOutcome = iota
	// deleteInFlight: the resource is DELETING.
	deleteInFlight
	// deleteGone: the resource is DELETED.
	deleteGone
	// deleteFailed: the resource is FAILED after a delete was accepted.
	deleteFailed
)

// failedDeletes is embedded by every resource type whose delete can end in lifecycle state
// FAILED. libnuke calls HandleWait on the same struct instance Remove() ran on, so the count
// survives from round to round.
//
// Filter() alone cannot report a FAILED delete. Filter() runs at scan time too, so it has to
// treat FAILED as present, or a FAILED resource is never deleted at all. And a present resource
// in a wait round leaves the item in ItemStateWaiting, a state libnuke never calls Remove() from
// again. The only way back to Remove() from a wait round is a plain error from a HandleWaitHook,
// which routes the item to ItemStateFailed; HandleQueue re-issues the delete from there.
type failedDeletes struct {
	count int
	last  string
	// accepted is set when OCI accepts a delete and cleared when its FAILED outcome is counted,
	// so one accepted delete is counted at most once.
	accepted bool
}

// gaveUp returns the leftover detail once maxFailedDeletes deletes have ended in FAILED, and ""
// before that.
func (f *failedDeletes) gaveUp() string {
	if f.count < maxFailedDeletes {
		return ""
	}
	return fmt.Sprintf("%s; gave up after %d failed deletes", f.last, f.count)
}

// refuse is the first thing Remove() calls. Once the cap is reached it returns ErrHoldResource
// instead of letting Remove() call OCI again. Hold, not a plain error: ItemStateFailed stops
// holding back whatever DependsOn this type, and the compartment delete would go out over a
// subtree that is not empty (resources/remove_error.go has the full argument). The item stays
// held, bounded by --max-wait-retries, and the leftover report carries the reason.
func (f *failedDeletes) refuse() error {
	if msg := f.gaveUp(); msg != "" {
		return liberrors.ErrHoldResource(msg)
	}
	return nil
}

// issued records the outcome of the delete call Remove() just made. It goes right before
// Remove()'s holdOn409, which stays the only thing deciding what Remove() returns.
func (f *failedDeletes) issued(err error) {
	if err == nil {
		f.accepted = true
	}
}

// wait maps one post-delete GET onto libnuke's HandleWaitHook contract. kind names the
// resource in messages ("load balancer"); detail is OCI's lifecycleDetails, empty if the type
// has none.
//
//   - 404 and DELETED return nil: libnuke's own List()/Filter() check then finds the resource
//     gone and finishes the item.
//   - DELETING returns ErrWaitResource, so the item keeps waiting, bounded by
//     --max-wait-retries, instead of converging the moment the delete is accepted. A delete
//     that later ends in FAILED is only visible to a poll that is still watching.
//   - A live state returns nil as well. libnuke's check then sees a present resource and leaves
//     the item's state alone, which keeps a 409-held item in ItemStateHold, the state that
//     re-issues the delete. Waiting here would park it where Remove() is never called again.
//   - FAILED returns a plain error and counts against maxFailedDeletes. Past the cap it returns
//     ErrWaitResource instead, for the same reason refuse() holds. FAILED with no delete accepted
//     since the last count -- the re-delete was refused with a 409 and is held -- returns nil
//     like a live state: nothing new failed, and the hold re-issues the delete.
//   - Any other GET error is returned as is. The item goes to ItemStateFailed, and a GET that
//     keeps failing trips libnuke's handleFailure abort like any other failure.
func (f *failedDeletes) wait(kind string, outcome deleteOutcome, detail string, err error) error {
	if err != nil {
		if svcErr, ok := common.IsServiceError(err); ok && svcErr.GetHTTPStatusCode() == http.StatusNotFound {
			return nil
		}
		return fmt.Errorf("reading %s after delete: %w", kind, err)
	}

	switch outcome {
	case deleteInFlight:
		return liberrors.ErrWaitResource(kind + " is DELETING")
	case deleteFailed:
		if msg := f.gaveUp(); msg != "" {
			return liberrors.ErrWaitResource(msg)
		}
		if !f.accepted {
			return nil
		}
		f.accepted = false
		f.count++
		f.last = kind + " is FAILED after delete"
		if detail != "" {
			f.last += ": " + detail
		}
		return fmt.Errorf("%s (failed delete %d of %d)", f.last, f.count, maxFailedDeletes)
	default:
		return nil
	}
}
