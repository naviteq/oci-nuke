package ocinuke_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ekristen/libnuke/pkg/filter"
	libnuke "github.com/ekristen/libnuke/pkg/nuke"
	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/ekristen/libnuke/pkg/resource"
	"github.com/ekristen/libnuke/pkg/scanner"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/plan"
)

// This file closes 04-CONTEXT.md's environment-note substitution for the third stubbed DoD item:
// "deleting a versioned bucket with an uncommitted multipart upload succeeds" -- proven against a
// stubbed Object Storage shape through the real libnuke.Nuke.Run() control flow, per
// resources/object_version.go's own live-verified fact (04-RESEARCH.md Q4): a real bucket found 1
// current object but 24 total ListObjectVersions entries. The genuinely live, seeded-sandbox
// destructive run remains outstanding, deferred to NR-682's e2e harness.

const (
	versionedBucketRegion        = "us-ashburn-1"
	versionedBucketCompartmentID = "ocid1.compartment.oc1..versioned-bucket-test"
	versionedBucketOwner         = versionedBucketRegion + "/" + versionedBucketCompartmentID

	fixtureBucketType          = "FixtureBucket"
	fixtureObjectVersionType   = "FixtureObjectVersion"
	fixtureMultipartUploadType = "FixtureMultipartUpload"

	// versionedBucketObjectVersionCount mirrors 04-RESEARCH.md Q4's live-verified real bucket
	// shape: ListObjectVersions returned 24 total object versions for a bucket ListObjects showed
	// only 1 current object for.
	versionedBucketObjectVersionCount = 24
	// versionedBucketTotalResourceCount is this test's non-vacuousness guard: 24 object versions
	// + 1 multipart upload + 1 bucket.
	versionedBucketTotalResourceCount = versionedBucketObjectVersionCount + 1 + 1

	fixtureBucketKey = "FixtureBucket-1"
)

// versionedLeafResource is the shared shape for both FixtureObjectVersion and
// FixtureMultipartUpload instances -- each Remove() records its own UniqueKey() into the shared,
// mutex-guarded recorder (reusing equivalence_test.go's own recorder type in this same package)
// before flipping removed to true, so the lister excludes it on every subsequent re-list
// (mirroring recordingResource/recordingLister's already-established convention).
type versionedLeafResource struct {
	uid           string
	compartmentID string
	recorder      *recorder
	removed       *bool
}

func (r *versionedLeafResource) Remove(_ context.Context) error {
	r.recorder.record(r.uid)
	*r.removed = true
	return nil
}
func (r *versionedLeafResource) UniqueKey() string        { return r.uid }
func (r *versionedLeafResource) GetCompartmentID() string { return r.compartmentID }
func (r *versionedLeafResource) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	return nil, nil, time.Time{}
}

type versionedLeafLister struct{ instances []*versionedLeafResource }

func (l *versionedLeafLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	out := make([]resource.Resource, 0, len(l.instances))
	for _, inst := range l.instances {
		if inst.removed != nil && *inst.removed {
			continue
		}
		out = append(out, inst)
	}
	return out, nil
}

// versionedBucketResource is FixtureBucket -- its own registry.Registration (built in
// registerVersionedBucketFixtures below) declares DependsOn: []string{fixtureObjectVersionType,
// fixtureMultipartUploadType}, mirroring resources/bucket.go's real DependsOn edge onto
// ObjectVersion/MultipartUpload (04-RESEARCH.md Q4). Remove() records into the SAME shared
// recorder as the leaf fixtures, so a single call-order slice proves the full ordering guarantee.
type versionedBucketResource struct {
	uid           string
	compartmentID string
	recorder      *recorder
	removed       *bool
}

func (r *versionedBucketResource) Remove(_ context.Context) error {
	r.recorder.record(r.uid)
	*r.removed = true
	return nil
}
func (r *versionedBucketResource) UniqueKey() string        { return r.uid }
func (r *versionedBucketResource) GetCompartmentID() string { return r.compartmentID }
func (r *versionedBucketResource) SafetyTags() (freeform, defined map[string]string, createdAt time.Time) {
	return nil, nil, time.Time{}
}

type versionedBucketLister struct{ instance *versionedBucketResource }

func (l *versionedBucketLister) List(_ context.Context, _ interface{}) ([]resource.Resource, error) {
	if l.instance.removed != nil && *l.instance.removed {
		return nil, nil
	}
	return []resource.Resource{l.instance}, nil
}

// registerVersionedBucketFixtures registers all three fixture types against a fresh registry,
// mirroring equivalence_test.go's registerEquivalenceFixtures shape. Every ocinuke.Register call
// uses an unconditionally-true inScope and a nil onSkip -- scope membership and leftover
// reporting are not what this test is about, matching equivalence_test.go's own convention.
// Parameters.WaitOnDependencies: true is set on the returned *libnuke.Nuke by
// newVersionedBucketNuke, not here -- this is the first test in resources_test to exercise a REAL
// DependsOn edge through WaitOnDependencies end to end (every prior fixture in
// equivalence_test.go/scheduled_deletion_test.go had DependsOn: nil).
func registerVersionedBucketFixtures(t *testing.T, rec *recorder) {
	t.Helper()
	registry.ClearRegistry()
	t.Cleanup(restoreRealRegistry)

	inScope := func(string) bool { return true }

	versions := make([]*versionedLeafResource, 0, versionedBucketObjectVersionCount)
	for i := 0; i < versionedBucketObjectVersionCount; i++ {
		versions = append(versions, &versionedLeafResource{
			uid:           fmt.Sprintf("FixtureObjectVersion-%d", i+1),
			compartmentID: versionedBucketCompartmentID,
			recorder:      rec,
			removed:       new(bool),
		})
	}
	ocinuke.Register(&registry.Registration{
		Name:     fixtureObjectVersionType,
		Scope:    ocinuke.CompartmentScope,
		Resource: versions[0],
		Lister:   &versionedLeafLister{instances: versions},
		// DependsOn is intentionally empty -- ObjectVersion is a leaf; FixtureBucket (below)
		// declares the edge on itself, mirroring resources/bucket.go's real convention.
	}, inScope, nil)

	upload := &versionedLeafResource{
		uid:           "FixtureMultipartUpload-1",
		compartmentID: versionedBucketCompartmentID,
		recorder:      rec,
		removed:       new(bool),
	}
	ocinuke.Register(&registry.Registration{
		Name:     fixtureMultipartUploadType,
		Scope:    ocinuke.CompartmentScope,
		Resource: upload,
		Lister:   &versionedLeafLister{instances: []*versionedLeafResource{upload}},
		// DependsOn is intentionally empty -- MultipartUpload is a leaf; FixtureBucket declares
		// the edge.
	}, inScope, nil)

	bucket := &versionedBucketResource{
		uid:           fixtureBucketKey,
		compartmentID: versionedBucketCompartmentID,
		recorder:      rec,
		removed:       new(bool),
	}
	ocinuke.Register(&registry.Registration{
		Name:      fixtureBucketType,
		Scope:     ocinuke.CompartmentScope,
		Resource:  bucket,
		Lister:    &versionedBucketLister{instance: bucket},
		DependsOn: []string{fixtureObjectVersionType, fixtureMultipartUploadType},
	}, inScope, nil)
}

// newVersionedBucketNuke mirrors equivalence_test.go's newEquivalenceNuke shape, adapted for
// three registered fixture types with Parameters.WaitOnDependencies: true set -- the real
// DependsOn edge above only orders removal when this is set (04-CONTEXT.md's own libnuke fact:
// DependsOn alone orders scanning only).
func newVersionedBucketNuke(t *testing.T) *libnuke.Nuke {
	t.Helper()

	n := libnuke.New(&libnuke.Parameters{
		NoDryRun:           true,
		Force:              true,
		ForceSleep:         3, // Nuke.Validate() rejects anything below 3
		WaitOnDependencies: true,
	}, filter.Filters{}, nil)
	n.SetRunSleep(1 * time.Millisecond) // libnuke's runSleep defaults to 5s; keep the test fast

	s, err := scanner.New(&scanner.Config{
		Owner:         versionedBucketOwner,
		ResourceTypes: []string{fixtureBucketType, fixtureObjectVersionType, fixtureMultipartUploadType},
		Opts:          &ocinuke.ListerOpts{Region: versionedBucketRegion, CompartmentID: versionedBucketCompartmentID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.RegisterScanner(ocinuke.CompartmentScope, s); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestVersionedBucketWithMultipartUpload_DeletesSuccessfully is the direct proof for
// 04-CONTEXT.md's environment-note substitution of "deleting a versioned bucket with an
// uncommitted multipart upload succeeds": a single --no-dry-run run removes all 24 object
// versions and the 1 multipart upload BEFORE FixtureBucket.Remove() is ever called (asserted via
// the shared recorder's own call order, not merely a passing Run()), and the whole 26-resource
// chain converges to zero leftovers without a manual second pass.
func TestVersionedBucketWithMultipartUpload_DeletesSuccessfully(t *testing.T) {
	rec := &recorder{}
	registerVersionedBucketFixtures(t, rec)

	n := newVersionedBucketNuke(t)
	if err := n.Run(context.Background()); err != nil {
		t.Fatalf("n.Run(ctx) returned error: %v", err)
	}

	order := rec.snapshot()
	if len(order) != versionedBucketTotalResourceCount {
		t.Fatalf("recorder captured %d Remove() calls, want %d (24 object versions + 1 multipart "+
			"upload + 1 bucket): %v", len(order), versionedBucketTotalResourceCount, order)
	}

	bucketIndex := -1
	for i, key := range order {
		if key == fixtureBucketKey {
			bucketIndex = i
		}
	}
	if bucketIndex == -1 {
		t.Fatal("FixtureBucket-1's Remove() was never called")
	}
	if bucketIndex != len(order)-1 {
		t.Fatalf("FixtureBucket-1's Remove() was call #%d of %d, want LAST (position %d) -- "+
			"WaitOnDependencies must ensure every object version and the multipart upload is "+
			"removed before the bucket delete is attempted (call order: %v)",
			bucketIndex+1, len(order), len(order)-1, order)
	}

	entries := plan.BuildFromQueue(n.Queue.GetItems(), true, plan.ClassifyLeftover)
	if len(entries) != versionedBucketTotalResourceCount {
		t.Fatalf("got %d plan entries, want %d (non-vacuousness guard): %+v", len(entries), versionedBucketTotalResourceCount, entries)
	}

	leftovers := entriesWithState(entries, plan.StateLeftover)
	if len(leftovers) != 0 {
		t.Fatalf("got %d leftover entries, want 0 -- the whole 26-resource chain must converge "+
			"without a manual second pass (entries: %+v)", len(leftovers), leftovers)
	}

	removed := entriesWithState(entries, plan.StateRemoved)
	if len(removed) != versionedBucketTotalResourceCount {
		t.Fatalf("got %d removed entries, want all %d resources removed (entries: %+v)", len(removed), versionedBucketTotalResourceCount, entries)
	}
}
