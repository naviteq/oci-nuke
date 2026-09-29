package resources

import (
	"context"
	"testing"
	"time"

	"github.com/ekristen/libnuke/pkg/registry"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/ons"
)

// stubNotificationTopicClient implements notificationTopicClient against in-memory data -- zero
// network access, mirroring stubInstanceClient's own established seam shape.
type stubNotificationTopicClient struct {
	items   []ons.NotificationTopicSummary
	deleted []string
	listErr error
}

func (s *stubNotificationTopicClient) ListTopics(
	_ context.Context,
	_ ons.ListTopicsRequest,
) (ons.ListTopicsResponse, error) {
	if s.listErr != nil {
		return ons.ListTopicsResponse{}, s.listErr
	}
	return ons.ListTopicsResponse{Items: s.items}, nil
}

func (s *stubNotificationTopicClient) DeleteTopic(
	_ context.Context,
	req ons.DeleteTopicRequest,
) (ons.DeleteTopicResponse, error) {
	s.deleted = append(s.deleted, *req.TopicId)
	return ons.DeleteTopicResponse{}, nil
}

// TestNotificationTopicList_List proves notificationTopicList wraps every
// ons.NotificationTopicSummary returned by ListTopics as a NotificationTopic, threading
// compartmentID through the request, and that identity is keyed on TopicId (NOT Id --
// NotificationTopicSummary has no Id field at all).
func TestNotificationTopicList_List(t *testing.T) {
	topicID := testNotificationTopicID
	compartmentID := testCompartmentOCID
	name := "alerts"
	apiEndpoint := "https://notification.example.com/topics/topic"
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubNotificationTopicClient{
		items: []ons.NotificationTopicSummary{{
			TopicId:        &topicID,
			Name:           &name,
			CompartmentId:  &compartmentID,
			LifecycleState: ons.NotificationTopicSummaryLifecycleStateActive,
			TimeCreated:    &timeCreated,
			ApiEndpoint:    &apiEndpoint,
		}},
	}

	got, err := notificationTopicList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("notificationTopicList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("notificationTopicList() returned %d resources, want 1", len(got))
	}
	nt, ok := got[0].(*NotificationTopic)
	if !ok {
		t.Fatalf("notificationTopicList()[0] is %T, want *NotificationTopic", got[0])
	}
	if nt.GetCompartmentID() != compartmentID {
		t.Errorf("GetCompartmentID() = %q, want %q", nt.GetCompartmentID(), compartmentID)
	}
	if nt.UniqueKey() != topicID {
		t.Errorf("UniqueKey() = %q, want %q (must be TopicId, not Id)", nt.UniqueKey(), topicID)
	}
}

// TestNotificationTopic_Filter is table-driven over every
// ons.NotificationTopicSummaryLifecycleStateEnum value -- exactly THREE values exist (no Deleted
// value, unlike Stream/StreamPool). "Gone" for a NotificationTopic means absent from a subsequent
// ListTopics entirely.
func TestNotificationTopic_Filter(t *testing.T) {
	tests := []struct {
		state   ons.NotificationTopicSummaryLifecycleStateEnum
		present bool
	}{
		{ons.NotificationTopicSummaryLifecycleStateActive, true},
		{ons.NotificationTopicSummaryLifecycleStateCreating, false},
		{ons.NotificationTopicSummaryLifecycleStateDeleting, false},
	}

	for _, tc := range tests {
		t.Run(string(tc.state), func(t *testing.T) {
			r := &NotificationTopic{}
			r.topic.LifecycleState = tc.state
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

// TestNotificationTopic_Remove proves the SDK delete call fires with the right TopicId (not Id),
// against a client that never touches the network.
func TestNotificationTopic_Remove(t *testing.T) {
	topicID := testNotificationTopicID
	stub := &stubNotificationTopicClient{}
	r := &NotificationTopic{client: stub}
	r.topic.TopicId = &topicID

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != topicID {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, topicID)
	}
}

// TestNotificationTopicRegistration_DependsOnSubscription proves NotificationTopic declares
// DependsOn: ["Subscription"] exactly as 05-CONTEXT.md locks -- Subscriptions must be
// scanned/removed before the topic they are attached to.
func TestNotificationTopicRegistration_DependsOnSubscription(t *testing.T) {
	reg := registry.GetRegistration(NotificationTopicResourceType)
	if reg == nil {
		t.Fatalf("no registration found for %q", NotificationTopicResourceType)
	}
	want := []string{"Subscription"}
	if len(reg.DependsOn) != len(want) {
		t.Fatalf("NotificationTopic DependsOn = %v (len %d), want %v (len %d)", reg.DependsOn, len(reg.DependsOn), want, len(want))
	}
	for i, w := range want {
		if reg.DependsOn[i] != w {
			t.Errorf("NotificationTopic DependsOn[%d] = %q, want %q", i, reg.DependsOn[i], w)
		}
	}
}
