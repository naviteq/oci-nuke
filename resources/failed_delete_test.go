package resources

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	liberrors "github.com/ekristen/libnuke/pkg/errors"
	"github.com/ekristen/libnuke/pkg/filter"
	libnuke "github.com/ekristen/libnuke/pkg/nuke"
	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/ekristen/libnuke/pkg/scanner"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
	"github.com/oracle/oci-go-sdk/v65/filestorage"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/oracle/oci-go-sdk/v65/networkloadbalancer"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/plan"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// What a HandleWait return value means to libnuke v1.3.0's HandleWait.
const (
	waitFinishes = "fall through"       // nil: libnuke's own List()/Filter() check decides
	waitHolds    = "wait"               // ErrWaitResource: ItemStateWaiting
	waitFails    = "fail and re-delete" // plain error: ItemStateFailed, next round deletes again
)

func classifyWait(err error) string {
	var waitErr liberrors.ErrWaitResource
	switch {
	case err == nil:
		return waitFinishes
	case errors.As(err, &waitErr):
		return waitHolds
	default:
		return waitFails
	}
}

func TestFailedDeletes_Wait(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		outcome deleteOutcome
		err     error
		want    string
	}{
		{"live state", deleteNotStarted, nil, waitFinishes},
		{"DELETING", deleteInFlight, nil, waitHolds},
		{"DELETED", deleteGone, nil, waitFinishes},
		{"FAILED", deleteFailed, nil, waitFails},
		{"404", deleteNotStarted, &stubServiceError{statusCode: http.StatusNotFound}, waitFinishes},
		{"500", deleteNotStarted, &stubServiceError{statusCode: http.StatusInternalServerError}, waitFails},
	}
	for _, tc := range tests {
		f := &failedDeletes{accepted: true}
		if got := classifyWait(f.wait("thing", tc.outcome, "", tc.err)); got != tc.want {
			t.Errorf("%s: wait() would %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestFailedDeletes_FailedWithoutAcceptedDeleteIsNotCounted(t *testing.T) {
	t.Parallel()

	f := &failedDeletes{}
	if got := classifyWait(f.wait("thing", deleteFailed, "", nil)); got != waitFinishes {
		t.Errorf("FAILED with no accepted delete: wait() would %s, want %s", got, waitFinishes)
	}
	if f.count != 0 {
		t.Errorf("count = %d, want 0", f.count)
	}

	f.issued(nil)
	if got := classifyWait(f.wait("thing", deleteFailed, "", nil)); got != waitFails {
		t.Errorf("FAILED after an accepted delete: wait() would %s, want %s", got, waitFails)
	}
	if got := classifyWait(f.wait("thing", deleteFailed, "", nil)); got != waitFinishes {
		t.Errorf("FAILED read twice for one delete: wait() would %s, want %s", got, waitFinishes)
	}
	if f.count != 1 {
		t.Errorf("count = %d, want 1 -- one accepted delete is counted once", f.count)
	}
}

func TestFailedDeletes_RefusesAndHoldsAfterTheCap(t *testing.T) {
	t.Parallel()

	f := &failedDeletes{}
	for i := 0; i < maxFailedDeletes; i++ {
		if err := f.refuse(); err != nil {
			t.Fatalf("refuse() after %d failed deletes = %v, want nil", i, err)
		}
		f.issued(nil)
		if got := classifyWait(f.wait("thing", deleteFailed, "backend busy", nil)); got != waitFails {
			t.Fatalf("failed delete %d: wait() would %s, want %s", i+1, got, waitFails)
		}
	}

	var hold liberrors.ErrHoldResource
	err := f.refuse()
	if !errors.As(err, &hold) {
		t.Fatalf("refuse() past the cap = %v, want ErrHoldResource -- a plain error stops holding "+
			"back the compartment", err)
	}
	for _, want := range []string{"thing is FAILED after delete: backend busy", "gave up after 3 failed deletes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refuse() = %q, want it to contain %q", err, want)
		}
	}
	if got := classifyWait(f.wait("thing", deleteFailed, "", nil)); got != waitHolds {
		t.Errorf("FAILED past the cap: wait() would %s, want %s", got, waitHolds)
	}
}

// TestHandleWait_MapsEachTypesLifecycleStates pins each type's state-to-outcome switch, so a
// wrong enum constant (the SDK has a Summary and a non-Summary enum for most of these) fails
// here rather than in a live run.
func TestHandleWait_MapsEachTypesLifecycleStates(t *testing.T) {
	t.Parallel()

	id := testResourceOCID
	type waiter interface{ HandleWait(context.Context) error }
	newLB := func(s loadbalancer.LoadBalancerLifecycleStateEnum) waiter {
		resp := loadbalancer.GetLoadBalancerResponse{LoadBalancer: loadbalancer.LoadBalancer{LifecycleState: s}}
		stub := &stubLoadBalancerClient{getFn: func() (loadbalancer.GetLoadBalancerResponse, error) { return resp, nil }}
		r := &LoadBalancer{client: stub, failedDeletes: failedDeletes{accepted: true}}
		r.lb.Id = &id
		return r
	}
	newNLB := func(s networkloadbalancer.LifecycleStateEnum) waiter {
		stub := &stubNetworkLoadBalancerClient{getResp: networkloadbalancer.GetNetworkLoadBalancerResponse{
			NetworkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{LifecycleState: s},
		}}
		r := &NetworkLoadBalancer{client: stub, failedDeletes: failedDeletes{accepted: true}}
		r.nlb.Id = &id
		return r
	}
	newMT := func(s filestorage.MountTargetLifecycleStateEnum) waiter {
		stub := &fakeFileStorageClient{getMountTargetResp: filestorage.GetMountTargetResponse{
			MountTarget: filestorage.MountTarget{LifecycleState: s},
		}}
		r := &MountTarget{client: stub, failedDeletes: failedDeletes{accepted: true}}
		r.target.Id = &id
		return r
	}
	newFS := func(s filestorage.FileSystemLifecycleStateEnum) waiter {
		stub := &fakeFileStorageClient{getFileSystemResp: filestorage.GetFileSystemResponse{
			FileSystem: filestorage.FileSystem{LifecycleState: s},
		}}
		r := &FileSystem{client: stub, failedDeletes: failedDeletes{accepted: true}}
		r.fs.Id = &id
		return r
	}
	newDVH := func(s core.DedicatedVmHostLifecycleStateEnum) waiter {
		stub := &stubDedicatedVmHostClient{getResp: core.GetDedicatedVmHostResponse{
			DedicatedVmHost: core.DedicatedVmHost{LifecycleState: s},
		}}
		r := &DedicatedVmHost{client: stub, failedDeletes: failedDeletes{accepted: true}}
		r.dedicatedVmHost.Id = &id
		return r
	}

	tests := []struct {
		name string
		r    waiter
		want string
	}{
		{"LoadBalancer ACTIVE", newLB(loadbalancer.LoadBalancerLifecycleStateActive), waitFinishes},
		{"LoadBalancer DELETING", newLB(loadbalancer.LoadBalancerLifecycleStateDeleting), waitHolds},
		{"LoadBalancer DELETED", newLB(loadbalancer.LoadBalancerLifecycleStateDeleted), waitFinishes},
		{"LoadBalancer FAILED", newLB(loadbalancer.LoadBalancerLifecycleStateFailed), waitFails},
		{"NetworkLoadBalancer ACTIVE", newNLB(networkloadbalancer.LifecycleStateActive), waitFinishes},
		{"NetworkLoadBalancer DELETING", newNLB(networkloadbalancer.LifecycleStateDeleting), waitHolds},
		{"NetworkLoadBalancer DELETED", newNLB(networkloadbalancer.LifecycleStateDeleted), waitFinishes},
		{"NetworkLoadBalancer FAILED", newNLB(networkloadbalancer.LifecycleStateFailed), waitFails},
		{"MountTarget ACTIVE", newMT(filestorage.MountTargetLifecycleStateActive), waitFinishes},
		{"MountTarget DELETING", newMT(filestorage.MountTargetLifecycleStateDeleting), waitHolds},
		{"MountTarget DELETED", newMT(filestorage.MountTargetLifecycleStateDeleted), waitFinishes},
		{"MountTarget FAILED", newMT(filestorage.MountTargetLifecycleStateFailed), waitFails},
		{"FileSystem ACTIVE", newFS(filestorage.FileSystemLifecycleStateActive), waitFinishes},
		{"FileSystem DELETING", newFS(filestorage.FileSystemLifecycleStateDeleting), waitHolds},
		{"FileSystem DELETED", newFS(filestorage.FileSystemLifecycleStateDeleted), waitFinishes},
		{"FileSystem FAILED", newFS(filestorage.FileSystemLifecycleStateFailed), waitFails},
		{"DedicatedVmHost ACTIVE", newDVH(core.DedicatedVmHostLifecycleStateActive), waitFinishes},
		{"DedicatedVmHost DELETING", newDVH(core.DedicatedVmHostLifecycleStateDeleting), waitHolds},
		{"DedicatedVmHost DELETED", newDVH(core.DedicatedVmHostLifecycleStateDeleted), waitFinishes},
		{"DedicatedVmHost FAILED", newDVH(core.DedicatedVmHostLifecycleStateFailed), waitFails},
	}
	for _, tc := range tests {
		if got := classifyWait(tc.r.HandleWait(context.Background())); got != tc.want {
			t.Errorf("%s: HandleWait() would %s, want %s", tc.name, got, tc.want)
		}
	}
}

// lbCloud plays OCI for one load balancer across a whole run: the state List() and
// GetLoadBalancer report, how many deletes are refused with a 409 before one is accepted, and
// the states successive GETs walk through after each accepted delete (the last one sticks).
// conflicts is set mid-run by the test that needs it.
type lbCloud struct {
	id          string
	state       loadbalancer.LoadBalancerLifecycleStateEnum
	conflicts   int
	afterDelete [][]loadbalancer.LoadBalancerLifecycleStateEnum
	deletes     int
	accepted    int
	pending     []loadbalancer.LoadBalancerLifecycleStateEnum
	stub        *stubLoadBalancerClient
}

func newLBCloud(
	state loadbalancer.LoadBalancerLifecycleStateEnum, afterDelete ...[]loadbalancer.LoadBalancerLifecycleStateEnum,
) *lbCloud {
	c := &lbCloud{id: testResourceOCID, state: state, afterDelete: afterDelete}
	c.stub = &stubLoadBalancerClient{
		deleteFn: func() error {
			c.deletes++
			if c.conflicts > 0 {
				c.conflicts--
				return &stubServiceError{statusCode: http.StatusConflict}
			}
			c.pending = c.afterDelete[min(c.accepted, len(c.afterDelete)-1)]
			c.accepted++
			return nil
		},
		getFn: func() (loadbalancer.GetLoadBalancerResponse, error) {
			if c.gone() {
				return loadbalancer.GetLoadBalancerResponse{}, &stubServiceError{statusCode: http.StatusNotFound}
			}
			if len(c.pending) > 0 {
				c.state = c.pending[0]
				if len(c.pending) > 1 {
					c.pending = c.pending[1:]
				}
			}
			return loadbalancer.GetLoadBalancerResponse{LoadBalancer: c.snapshot()}, nil
		},
	}
	return c
}

func (c *lbCloud) gone() bool { return c.state == loadbalancer.LoadBalancerLifecycleStateDeleted }

func (c *lbCloud) snapshot() loadbalancer.LoadBalancer {
	compartmentID := testCompartmentOCID
	return loadbalancer.LoadBalancer{
		Id: &c.id, CompartmentId: &compartmentID, LifecycleState: c.state,
		TimeCreated: &common.SDKTime{Time: time.Now().Add(-time.Hour)},
	}
}

// List returns a fresh LoadBalancer each call, as the real lister does; libnuke matches it to
// the queued one by UniqueKey.
func (c *lbCloud) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	if c.gone() {
		return nil, nil
	}
	return []resource.Resource{&LoadBalancer{client: c.stub, lb: c.snapshot()}}, nil
}

// runLBCloud drives the REAL LoadBalancer type through a REAL libnuke.Nuke.Run(), with only the
// SDK client scripted -- the compartment_integration_test.go shape. The fixture registers under
// its own name so the package's real "LoadBalancer" registration stays untouched.
func runLBCloud(t *testing.T, fixtureType string, c *lbCloud, maxWaitRetries int) ([]plan.Entry, error) {
	t.Helper()

	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(*scope.SkipEvent) {})
	t.Cleanup(restore)

	ocinuke.Register(&registry.Registration{
		Name:     fixtureType,
		Scope:    ocinuke.CompartmentScope,
		Resource: &LoadBalancer{},
		Lister:   c,
	}, ocinuke.CurrentScope, ocinuke.CurrentReporter)

	n := libnuke.New(&libnuke.Parameters{
		NoDryRun:       true,
		Force:          true,
		ForceSleep:     3,
		MaxWaitRetries: maxWaitRetries,
	}, filter.Filters{}, nil)
	n.SetRunSleep(1 * time.Millisecond)

	s, err := scanner.New(&scanner.Config{
		Owner:         compartmentIntegrationTestRegion + "/" + testCompartmentOCID,
		ResourceTypes: []string{fixtureType},
		Opts:          &ocinuke.ListerOpts{Region: compartmentIntegrationTestRegion, CompartmentID: testCompartmentOCID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.RegisterScanner(ocinuke.CompartmentScope, s); err != nil {
		t.Fatal(err)
	}

	runErr := n.Run(context.Background())
	return plan.BuildFromQueue(n.Queue.GetItems(), true, plan.ClassifyLeftover), runErr
}

var (
	lbDeleting = loadbalancer.LoadBalancerLifecycleStateDeleting
	lbDeleted  = loadbalancer.LoadBalancerLifecycleStateDeleted
	lbFailed   = loadbalancer.LoadBalancerLifecycleStateFailed
	lbActive   = loadbalancer.LoadBalancerLifecycleStateActive
)

func TestLoadBalancerRun_FailedAtScanIsDeleted(t *testing.T) {
	c := newLBCloud(lbFailed, []loadbalancer.LoadBalancerLifecycleStateEnum{lbDeleting, lbDeleting, lbDeleted})

	entries, runErr := runLBCloud(t, "FailedDeleteFixtureFailedAtScan", c, 50)
	if runErr != nil {
		t.Fatalf("n.Run() = %v, want nil", runErr)
	}
	if c.deletes != 1 {
		t.Errorf("DeleteLoadBalancer called %d times, want 1", c.deletes)
	}
	if len(entries) != 1 || entries[0].State != plan.StateRemoved {
		t.Errorf("entries = %+v, want one %q entry", entries, plan.StateRemoved)
	}
}

func TestLoadBalancerRun_FailedDeleteIsRetried(t *testing.T) {
	c := newLBCloud(lbActive,
		[]loadbalancer.LoadBalancerLifecycleStateEnum{lbDeleting, lbFailed},
		[]loadbalancer.LoadBalancerLifecycleStateEnum{lbDeleting, lbDeleted},
	)

	entries, runErr := runLBCloud(t, "FailedDeleteFixtureRetried", c, 50)
	if runErr != nil {
		t.Fatalf("n.Run() = %v, want nil -- the second delete succeeded", runErr)
	}
	if c.deletes != 2 {
		t.Errorf("DeleteLoadBalancer called %d times, want 2 -- the FAILED delete must be re-issued", c.deletes)
	}
	if len(entries) != 1 || entries[0].State != plan.StateRemoved {
		t.Errorf("entries = %+v, want one %q entry", entries, plan.StateRemoved)
	}
}

// TestLoadBalancerRun_DeleteThatKeepsFailingFailsTheRun is the case the upstream issue was
// about: before the fix this run reported the load balancer finished and returned nil.
func TestLoadBalancerRun_DeleteThatKeepsFailingFailsTheRun(t *testing.T) {
	c := newLBCloud(lbActive, []loadbalancer.LoadBalancerLifecycleStateEnum{lbDeleting, lbFailed})

	entries, runErr := runLBCloud(t, "FailedDeleteFixtureKeepsFailing", c, 20)
	if runErr == nil {
		t.Fatal("n.Run() = nil, want an error -- a load balancer that is still FAILED was reported deleted")
	}
	if c.deletes != maxFailedDeletes {
		t.Errorf("DeleteLoadBalancer called %d times, want %d", c.deletes, maxFailedDeletes)
	}
	if len(entries) != 1 || entries[0].State != plan.StateLeftover {
		t.Fatalf("entries = %+v, want one %q entry", entries, plan.StateLeftover)
	}
	if !strings.Contains(entries[0].Detail, "gave up after 3 failed deletes") {
		t.Errorf("entries[0].Detail = %q, want the give-up reason", entries[0].Detail)
	}
}

// TestLoadBalancerRun_ConflictOnRedeleteKeepsRetrying covers the hold path: a re-delete of a
// FAILED load balancer refused with a 409 must stay held and be re-issued, not be parked in
// ItemStateWaiting (where libnuke never calls Remove() again) or counted as another failure.
func TestLoadBalancerRun_ConflictOnRedeleteKeepsRetrying(t *testing.T) {
	c := newLBCloud(lbActive,
		[]loadbalancer.LoadBalancerLifecycleStateEnum{lbDeleting, lbFailed},
		[]loadbalancer.LoadBalancerLifecycleStateEnum{lbDeleting, lbDeleted},
	)
	refuseAfterFirst := c.stub.deleteFn
	c.stub.deleteFn = func() error {
		if c.deletes == 1 {
			c.conflicts = 2
		}
		return refuseAfterFirst()
	}

	entries, runErr := runLBCloud(t, "FailedDeleteFixtureConflict", c, 50)
	if runErr != nil {
		t.Fatalf("n.Run() = %v, want nil", runErr)
	}
	if c.deletes != 4 {
		t.Errorf("DeleteLoadBalancer called %d times, want 4 (accepted, 409, 409, accepted)", c.deletes)
	}
	if len(entries) != 1 || entries[0].State != plan.StateRemoved {
		t.Errorf("entries = %+v, want one %q entry", entries, plan.StateRemoved)
	}
}
