package resources

import (
	"context"
	"testing"
	"time"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/containerengine"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// stubClusterClient implements clusterClient against in-memory data -- zero network access,
// mirroring stubInstanceClient's established precedent (resources/instance_test.go).
type stubClusterClient struct {
	items   []containerengine.ClusterSummary
	deleted []string
	listErr error
}

func (s *stubClusterClient) ListClusters(
	_ context.Context,
	_ containerengine.ListClustersRequest,
) (containerengine.ListClustersResponse, error) {
	if s.listErr != nil {
		return containerengine.ListClustersResponse{}, s.listErr
	}
	return containerengine.ListClustersResponse{Items: s.items}, nil
}

func (s *stubClusterClient) DeleteCluster(
	_ context.Context,
	req containerengine.DeleteClusterRequest,
) (containerengine.DeleteClusterResponse, error) {
	s.deleted = append(s.deleted, *req.ClusterId)
	return containerengine.DeleteClusterResponse{}, nil
}

// TestClusterLister_List proves clusterList returns every item ListClusters gives back,
// wrapped, without ever constructing a real ContainerEngine client.
func TestClusterLister_List(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID

	stub := &stubClusterClient{
		items: []containerengine.ClusterSummary{
			{Id: &id, CompartmentId: &compartmentID, LifecycleState: containerengine.ClusterLifecycleStateActive},
		},
	}

	got, err := clusterList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("clusterList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("clusterList() returned %d resources, want 1", len(got))
	}
	c, ok := got[0].(*Cluster)
	if !ok {
		t.Fatalf("clusterList()[0] is %T, want *Cluster", got[0])
	}
	if c.GetCompartmentID() != compartmentID {
		t.Errorf("GetCompartmentID() = %q, want %q", c.GetCompartmentID(), compartmentID)
	}
	if c.UniqueKey() != id {
		t.Errorf("UniqueKey() = %q, want %q", c.UniqueKey(), id)
	}
}

// TestCluster_GetCompartmentID_NilSafe proves a nil CompartmentId (ClusterSummary.CompartmentId
// is mandatory:"false") returns "" rather than panicking -- scopedLister then drops the
// resource fail-closed (T-05-01-03).
func TestCluster_GetCompartmentID_NilSafe(t *testing.T) {
	r := &Cluster{}
	if got := r.GetCompartmentID(); got != "" {
		t.Errorf("GetCompartmentID() with nil CompartmentId = %q, want \"\"", got)
	}
}

// TestCluster_UniqueKey_NilSafe proves a nil Id (ClusterSummary.Id is mandatory:"false") returns
// "" rather than panicking -- scopedLister then drops the resource fail-closed (T-05-01-03).
func TestCluster_UniqueKey_NilSafe(t *testing.T) {
	r := &Cluster{}
	if got := r.UniqueKey(); got != "" {
		t.Errorf("UniqueKey() with nil Id = %q, want \"\"", got)
	}
}

// TestCluster_Filter is table-driven over every containerengine.ClusterLifecycleStateEnum value
// -- CREATING/ACTIVE/UPDATING/FAILED must return nil (present), and DELETING/DELETED must both
// return non-nil (excluded). This IS the regression test for the hang trap (T-05-01-02): a
// Filter() that forgets the DELETING branch fails this test immediately.
func TestCluster_Filter(t *testing.T) {
	tests := []struct {
		state   containerengine.ClusterLifecycleStateEnum
		present bool
	}{
		{containerengine.ClusterLifecycleStateCreating, true},
		{containerengine.ClusterLifecycleStateActive, true},
		{containerengine.ClusterLifecycleStateUpdating, true},
		{containerengine.ClusterLifecycleStateFailed, true},
		{containerengine.ClusterLifecycleStateDeleting, false},
		{containerengine.ClusterLifecycleStateDeleted, false},
		{containerengine.ClusterLifecycleStateEnum("SOME_FUTURE_STATE"), false},
	}

	for _, tc := range tests {
		r := &Cluster{}
		r.cluster.LifecycleState = tc.state
		err := r.Filter()
		if tc.present && err != nil {
			t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
		}
		if !tc.present && err == nil {
			t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
		}
	}
}

// TestCluster_SafetyTags_MetadataTimeCreated: the creation time comes from Metadata, so a
// cluster younger than min-age is protected; a record without metadata still yields zero.
func TestCluster_SafetyTags_MetadataTimeCreated(t *testing.T) {
	created := common.SDKTime{Time: time.Now().Add(-48 * time.Minute)}
	r := &Cluster{cluster: containerengine.ClusterSummary{Metadata: &containerengine.ClusterMetadata{TimeCreated: &created}}}

	freeform, defined, createdAt := r.SafetyTags()
	if !createdAt.Equal(created.Time) {
		t.Fatalf("createdAt = %v, want %v", createdAt, created.Time)
	}
	evt := ocinuke.Evaluate(testCompartmentOCID, ClusterResourceType, testResourceOCID, freeform, defined, createdAt,
		ocinuke.SafetyFilterConfig{MinAge: 2 * time.Hour})
	if evt == nil || evt.Reason != scope.ReasonTooYoung {
		t.Errorf("Evaluate() = %+v, want too-young", evt)
	}

	if _, _, zero := (&Cluster{}).SafetyTags(); !zero.IsZero() {
		t.Errorf("createdAt without metadata = %v, want zero", zero)
	}
}

// TestCluster_Remove proves the SDK delete call fires with the right parameter, against a client
// that never touches the network.
func TestCluster_Remove(t *testing.T) {
	id := testResourceOCID

	stub := &stubClusterClient{}
	r := &Cluster{client: stub}
	r.cluster.Id = &id

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}

// TestCluster_DependsOn_NodePool proves Cluster's registration declares DependsOn: ["NodePool"],
// so NodePool is always scanned and removed before its owning Cluster.
func TestCluster_DependsOn_NodePool(t *testing.T) {
	reg := registry.GetRegistration(ClusterResourceType)
	if reg == nil {
		t.Fatalf("no registration found for %q", ClusterResourceType)
	}
	want := []string{"NodePool"}
	if len(reg.DependsOn) != len(want) {
		t.Fatalf("Cluster DependsOn = %v (len %d), want %v (len %d)", reg.DependsOn, len(reg.DependsOn), want, len(want))
	}
	for i, w := range want {
		if reg.DependsOn[i] != w {
			t.Errorf("Cluster DependsOn[%d] = %q, want %q", i, reg.DependsOn[i], w)
		}
	}
}
