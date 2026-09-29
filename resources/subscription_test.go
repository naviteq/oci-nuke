package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/ons"
)

// stubSubscriptionClient implements subscriptionClient against in-memory data -- zero network
// access, mirroring stubInstanceClient's own established seam shape.
type stubSubscriptionClient struct {
	items   []ons.SubscriptionSummary
	deleted []string
	listErr error
}

func (s *stubSubscriptionClient) ListSubscriptions(
	_ context.Context,
	_ ons.ListSubscriptionsRequest,
) (ons.ListSubscriptionsResponse, error) {
	if s.listErr != nil {
		return ons.ListSubscriptionsResponse{}, s.listErr
	}
	return ons.ListSubscriptionsResponse{Items: s.items}, nil
}

func (s *stubSubscriptionClient) DeleteSubscription(
	_ context.Context,
	req ons.DeleteSubscriptionRequest,
) (ons.DeleteSubscriptionResponse, error) {
	s.deleted = append(s.deleted, *req.SubscriptionId)
	return ons.DeleteSubscriptionResponse{}, nil
}

// TestSubscriptionList_List proves subscriptionList wraps every ons.SubscriptionSummary returned
// by ListSubscriptions as a Subscription, threading compartmentID through the request.
func TestSubscriptionList_List(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	topicID := testNotificationTopicID
	protocol := "EMAIL"
	endpoint := "ops@example.com"

	stub := &stubSubscriptionClient{
		items: []ons.SubscriptionSummary{{
			Id:             &id,
			TopicId:        &topicID,
			Protocol:       &protocol,
			Endpoint:       &endpoint,
			CompartmentId:  &compartmentID,
			LifecycleState: ons.SubscriptionSummaryLifecycleStateActive,
		}},
	}

	got, err := subscriptionList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("subscriptionList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("subscriptionList() returned %d resources, want 1", len(got))
	}
	s, ok := got[0].(*Subscription)
	if !ok {
		t.Fatalf("subscriptionList()[0] is %T, want *Subscription", got[0])
	}
	if s.GetCompartmentID() != compartmentID {
		t.Errorf("GetCompartmentID() = %q, want %q", s.GetCompartmentID(), compartmentID)
	}
	if s.UniqueKey() != id {
		t.Errorf("UniqueKey() = %q, want %q", s.UniqueKey(), id)
	}
}

// TestSubscription_Filter is table-driven over every ons.SubscriptionSummaryLifecycleStateEnum
// value -- exactly THREE values exist (no DELETING intermediate state, unlike every other
// lifecycle-bearing type in this wave).
func TestSubscription_Filter(t *testing.T) {
	tests := []struct {
		state   ons.SubscriptionSummaryLifecycleStateEnum
		present bool
	}{
		{ons.SubscriptionSummaryLifecycleStatePending, true},
		{ons.SubscriptionSummaryLifecycleStateActive, true},
		{ons.SubscriptionSummaryLifecycleStateDeleted, false},
	}

	for _, tc := range tests {
		t.Run(string(tc.state), func(t *testing.T) {
			r := &Subscription{}
			r.subscription.LifecycleState = tc.state
			err := r.Filter()
			if tc.present && err != nil {
				t.Errorf("Filter() with present state %s = %v, want nil", tc.state, err)
			}
			if !tc.present && err == nil {
				t.Errorf("Filter() with excluded state %s = nil, want non-nil (hang-trap regression)", tc.state)
			}
		})
	}
}

// TestSubscription_Remove proves the SDK delete call fires with the right SubscriptionId,
// against a client that never touches the network.
func TestSubscription_Remove(t *testing.T) {
	id := testResourceOCID
	stub := &stubSubscriptionClient{}
	r := &Subscription{client: stub}
	r.subscription.Id = &id

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}

// TestSubscription_SafetyTags_CreatedTimeConversion proves subscriptionCreatedAt correctly
// converts ons.SubscriptionSummary.CreatedTime (Unix epoch milliseconds, mandatory:"false") to a
// time.Time, and that a nil CreatedTime returns the zero time.Time rather than panicking --
// ons.SubscriptionSummary has NO common.SDKTime TimeCreated field at all, a genuinely different
// shape from every other lifecycle-bearing type in this wave.
func TestSubscription_SafetyTags_CreatedTimeConversion(t *testing.T) {
	r := &Subscription{}
	_, _, createdAt := r.SafetyTags()
	if !createdAt.IsZero() {
		t.Errorf("SafetyTags() createdAt = %v, want zero time.Time for nil CreatedTime", createdAt)
	}

	millis := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	r.subscription.CreatedTime = &millis
	_, _, createdAt = r.SafetyTags()
	want := time.UnixMilli(millis)
	if !createdAt.Equal(want) {
		t.Errorf("SafetyTags() createdAt = %v, want %v", createdAt, want)
	}
}
