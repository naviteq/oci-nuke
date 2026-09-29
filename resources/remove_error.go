package resources

import (
	"net/http"
	"strings"

	liberrors "github.com/ekristen/libnuke/pkg/errors"
	"github.com/oracle/oci-go-sdk/v65/common"
)

// holdOn409 is what every Remove() in this package returns its error through. An OCI 409 on a
// delete means "not from this state, not right now" -- BucketNotEmpty, IncorrectState, Conflict,
// CompartmentNotEmpty -- and never "never". It is the condition --max-wait-retries exists to ride
// out, and the item must keep blocking whatever DependsOn it while it does.
//
// The status code is the boundary, not the service code. An allowlist of service codes would be
// visible in one place but has to be chased as OCI adds services, and every code nobody thought
// of reintroduces the deadlock below. Everything that is not a 409 stays terminal.
//
// 429 and 5xx are deliberately NOT here: oci-go-sdk's DefaultRetryPolicy (attached in pkg/clients)
// has already retried and given up on those beneath us, and a second retry layer would multiply
// wall-clock against a failure the SDK already called final.
//
// ── Why ErrHoldResource and not ErrWaitResource ────────────────────────────────────────────────
//
// NR-787's own analysis said ErrWaitResource. Read against libnuke v1.3.0 that is wrong, and it
// fails silently -- which is the whole shape of the bug it was meant to fix. HandleRemove
// (pkg/nuke/nuke.go:606) is the only handler that sees a Remove() error, and it maps exactly one
// error type: ErrHoldResource -> ItemStateHold. ErrWaitResource is not matched there at all and
// falls through to ItemStateFailed, the very state that stops holding back dependents.
// ErrWaitResource is only honored from a HandleWaitHook (nuke.go:654), which is why
// Compartment.HandleWait returns it and Remove() must not.
//
// ItemStateHold is the state with the three properties this needs, all verified in v1.3.0:
//   - HandleWaitDependency (nuke.go:633) counts Hold, so a held child still blocks its parent.
//     ItemStateFailed is absent from that list -- the defect: one refused DeleteBucket and
//     DeleteCompartment goes out six seconds later over a subtree that is not empty.
//   - HandleQueue (nuke.go:561) routes Hold back through HandleRemove every round, so the delete
//     is genuinely retried rather than merely observed.
//   - handleWaiting (nuke.go:289) counts Hold in pendingCount, so it is bounded by
//     MaxWaitRetries like any other wait. ocinuke.ReportLeftover's own doc comment calls
//     ErrHoldResource unbounded; that holds only for MaxWaitRetries == 0, libnuke's
//     retry-indefinitely sentinel, which run/command.go floors away for compartment deletes.
func holdOn409(err error) error {
	if err == nil {
		return nil
	}

	svcErr, ok := common.IsServiceError(err)
	if !ok || svcErr.GetHTTPStatusCode() != http.StatusConflict {
		return err
	}

	// The API's own words, because item.Reason is what the leftover table's DETAIL column
	// eventually prints and "409" alone does not distinguish BucketNotEmpty from IncorrectState.
	var b strings.Builder
	b.WriteString("HTTP 409")
	if code := svcErr.GetCode(); code != "" {
		b.WriteString(" ")
		b.WriteString(code)
	}
	if msg := svcErr.GetMessage(); msg != "" {
		b.WriteString(": ")
		b.WriteString(msg)
	}
	return liberrors.ErrHoldResource(b.String())
}
