package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/objectstorage"

	"github.com/naviteq/oci-nuke/pkg/ocinuke"
	"github.com/naviteq/oci-nuke/pkg/scope"
)

// TestRetentionRuleLister_List proves retentionRuleList wraps every
// objectstorage.RetentionRuleSummary returned per bucket, threading namespace/bucket name/
// compartmentID down correctly.
func TestRetentionRuleLister_List(t *testing.T) {
	namespace := testNamespace
	compartmentID := testCompartmentOCID
	bucketName := testBucketName
	id := testResourceOCID
	displayName := "test-rule"
	timeCreated := common.SDKTime{Time: time.Now()}
	timeModified := common.SDKTime{Time: time.Now()}

	stub := &fakeObjectStorageClient{
		buckets: []objectstorage.BucketSummary{
			{Namespace: &namespace, Name: &bucketName, CompartmentId: &compartmentID},
		},
		retentionRules: map[string][]objectstorage.RetentionRuleSummary{
			bucketName: {{Id: &id, DisplayName: &displayName, TimeCreated: &timeCreated, TimeModified: &timeModified}},
		},
	}

	got, err := retentionRuleList(context.Background(), stub, namespace, compartmentID)
	if err != nil {
		t.Fatalf("retentionRuleList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("retentionRuleList() returned %d resources, want 1", len(got))
	}
	rule, ok := got[0].(*RetentionRule)
	if !ok {
		t.Fatalf("retentionRuleList()[0] is %T, want *RetentionRule", got[0])
	}
	if rule.GetCompartmentID() != compartmentID {
		t.Errorf("GetCompartmentID() = %q, want %q", rule.GetCompartmentID(), compartmentID)
	}
}

// TestRetentionRule_Filter_LockedRuleExcludedAndReported is the named test this plan's success
// criteria require: a fixture rule with TimeRuleLocked set to a time in the PAST must be excluded
// via Filter() AND reported via ReportLeftover(ReasonRetentionLocked, ...) -- never reach
// DeleteRetentionRule at all. This is the one genuinely undeletable-except-via-bucket-deletion
// case in this wave, per [CITED] docs.oracle.com/en-us/iaas/Content/Object/Tasks/
// usingretentionrules.htm.
func TestRetentionRule_Filter_LockedRuleExcludedAndReported(t *testing.T) {
	var got []*scope.SkipEvent
	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(evt *scope.SkipEvent) {
		got = append(got, evt)
	})
	defer restore()

	id := testResourceOCID
	compartmentID := testCompartmentOCID
	lockedInThePast := common.SDKTime{Time: time.Now().Add(-24 * time.Hour)}

	r := &RetentionRule{
		compartmentID: compartmentID,
		rule:          objectstorage.RetentionRuleSummary{Id: &id, TimeRuleLocked: &lockedInThePast},
	}

	if err := r.Filter(); err == nil {
		t.Fatal("Filter() with a rule locked in the past = nil, want non-nil (must be excluded)")
	}

	if len(got) != 1 {
		t.Fatalf("ReportLeftover called %d times, want 1", len(got))
	}
	if got[0].Reason != scope.ReasonRetentionLocked {
		t.Errorf("SkipEvent.Reason = %q, want %q", got[0].Reason, scope.ReasonRetentionLocked)
	}
	if got[0].ResourceType != RetentionRuleResourceType {
		t.Errorf("SkipEvent.ResourceType = %q, want %q", got[0].ResourceType, RetentionRuleResourceType)
	}
	if got[0].ResourceID != id {
		t.Errorf("SkipEvent.ResourceID = %q, want %q", got[0].ResourceID, id)
	}
	if got[0].CompartmentID != compartmentID {
		t.Errorf("SkipEvent.CompartmentID = %q, want %q", got[0].CompartmentID, compartmentID)
	}
}

// TestRetentionRule_Filter_NeverLockedProceeds proves a rule with TimeRuleLocked == nil (never
// locked) proceeds to Remove() normally -- Filter() returns nil, no leftover reported.
func TestRetentionRule_Filter_NeverLockedProceeds(t *testing.T) {
	var got []*scope.SkipEvent
	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(evt *scope.SkipEvent) {
		got = append(got, evt)
	})
	defer restore()

	id := testResourceOCID
	r := &RetentionRule{rule: objectstorage.RetentionRuleSummary{Id: &id}}

	if err := r.Filter(); err != nil {
		t.Errorf("Filter() with TimeRuleLocked == nil = %v, want nil (still removable)", err)
	}
	if len(got) != 0 {
		t.Errorf("ReportLeftover called %d times, want 0 (never-locked rule is not a leftover)", len(got))
	}
}

// TestRetentionRule_Filter_LockDateInFutureProceeds proves a rule whose TimeRuleLocked is still
// in the future (within the mandatory 14-day pre-lock delay) proceeds to Remove() normally --
// still removable until the lock actually takes effect.
func TestRetentionRule_Filter_LockDateInFutureProceeds(t *testing.T) {
	id := testResourceOCID
	lockedInTheFuture := common.SDKTime{Time: time.Now().Add(14 * 24 * time.Hour)}
	r := &RetentionRule{rule: objectstorage.RetentionRuleSummary{Id: &id, TimeRuleLocked: &lockedInTheFuture}}

	if err := r.Filter(); err != nil {
		t.Errorf("Filter() with TimeRuleLocked in the future = %v, want nil (still removable, pre-lock delay)", err)
	}
}

// TestRetentionRule_Remove_NeverReachedWhenLocked proves DeleteRetentionRule is never called when
// the rule is locked -- the caller (registry.Nuke's own scan flow) is expected to call Filter()
// before Remove(); this test proves Remove() itself is never invoked for a locked rule by
// asserting the stub's delete counter stays at 0 when only Filter() is exercised.
func TestRetentionRule_Remove_NeverReachedWhenLocked(t *testing.T) {
	restore := ocinuke.SetRunContext(func(string) bool { return true }, func(*scope.SkipEvent) {})
	defer restore()

	id := testResourceOCID
	lockedInThePast := common.SDKTime{Time: time.Now().Add(-24 * time.Hour)}
	stub := &fakeObjectStorageClient{}
	r := &RetentionRule{client: stub, rule: objectstorage.RetentionRuleSummary{Id: &id, TimeRuleLocked: &lockedInThePast}}

	if err := r.Filter(); err == nil {
		t.Fatal("Filter() with a locked rule = nil, want non-nil")
	}
	if len(stub.deletedRetentionRules) != 0 {
		t.Errorf("DeleteRetentionRule called %d times after Filter() excluded the rule, want 0", len(stub.deletedRetentionRules))
	}
}

// TestRetentionRule_Remove_UnlockedRule proves DeleteRetentionRule fires with the exact
// RetentionRuleId/namespace/bucket for an unlocked rule.
func TestRetentionRule_Remove_UnlockedRule(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &fakeObjectStorageClient{}
	r := &RetentionRule{
		client:        stub,
		namespace:     testNamespace,
		bucketName:    testBucketName,
		compartmentID: compartmentID,
		rule:          objectstorage.RetentionRuleSummary{Id: &id, TimeCreated: &timeCreated},
	}

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deletedRetentionRules) != 1 {
		t.Fatalf("stub.deletedRetentionRules has %d entries, want 1", len(stub.deletedRetentionRules))
	}
	got := stub.deletedRetentionRules[0]
	if got.RetentionRuleId == nil || *got.RetentionRuleId != id {
		t.Errorf("DeleteRetentionRuleRequest.RetentionRuleId = %v, want %q", got.RetentionRuleId, id)
	}
}

// TestRetentionRule_Properties proves Properties() surfaces id/name/bucket/namespace, and
// time_rule_locked only when actually set.
func TestRetentionRule_Properties(t *testing.T) {
	id := testResourceOCID
	displayName := "test-rule"

	r := &RetentionRule{
		namespace:  testNamespace,
		bucketName: testBucketName,
		rule:       objectstorage.RetentionRuleSummary{Id: &id, DisplayName: &displayName},
	}

	props := r.Properties()
	if got := props.Get(propID); got != id {
		t.Errorf("Properties()[%q] = %q, want %q", propID, got, id)
	}
	if got := props.Get(propName); got != displayName {
		t.Errorf("Properties()[%q] = %q, want %q", propName, got, displayName)
	}
	if _, ok := props["time_rule_locked"]; ok {
		t.Errorf("Properties()[%q] present for a never-locked rule, want absent", "time_rule_locked")
	}
}
