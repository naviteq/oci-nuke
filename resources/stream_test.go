package resources

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/streaming"
)

// stubStreamClient implements streamClient against in-memory data -- zero network access,
// mirroring stubInstanceClient's own established seam shape.
type stubStreamClient struct {
	items   []streaming.StreamSummary
	deleted []string
	listErr error
}

func (s *stubStreamClient) ListStreams(
	_ context.Context,
	_ streaming.ListStreamsRequest,
) (streaming.ListStreamsResponse, error) {
	if s.listErr != nil {
		return streaming.ListStreamsResponse{}, s.listErr
	}
	return streaming.ListStreamsResponse{Items: s.items}, nil
}

func (s *stubStreamClient) DeleteStream(
	_ context.Context,
	req streaming.DeleteStreamRequest,
) (streaming.DeleteStreamResponse, error) {
	s.deleted = append(s.deleted, *req.StreamId)
	return streaming.DeleteStreamResponse{}, nil
}

// TestStreamList_List proves streamList wraps every streaming.StreamSummary returned by
// ListStreams as a Stream, threading compartmentID through the request via CompartmentId (never
// StreamPoolId).
func TestStreamList_List(t *testing.T) {
	id := testResourceOCID
	compartmentID := testCompartmentOCID
	streamPoolID := "ocid1.streampool.oc1..pool"
	name := "TelemetryEvents"
	timeCreated := common.SDKTime{Time: time.Now()}

	stub := &stubStreamClient{
		items: []streaming.StreamSummary{{
			Id:             &id,
			Name:           &name,
			CompartmentId:  &compartmentID,
			StreamPoolId:   &streamPoolID,
			LifecycleState: streaming.StreamSummaryLifecycleStateActive,
			TimeCreated:    &timeCreated,
		}},
	}

	got, err := streamList(context.Background(), stub, compartmentID)
	if err != nil {
		t.Fatalf("streamList() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("streamList() returned %d resources, want 1", len(got))
	}
	s, ok := got[0].(*Stream)
	if !ok {
		t.Fatalf("streamList()[0] is %T, want *Stream", got[0])
	}
	if s.GetCompartmentID() != compartmentID {
		t.Errorf("GetCompartmentID() = %q, want %q", s.GetCompartmentID(), compartmentID)
	}
	if s.UniqueKey() != id {
		t.Errorf("UniqueKey() = %q, want %q", s.UniqueKey(), id)
	}
}

// TestStreamList_UsesCompartmentIdNeverStreamPoolId proves the ListStreamsRequest built by
// streamList carries only CompartmentId, never StreamPoolId -- ListStreamsRequest.CompartmentId
// is mandatory:"false" and exclusive with StreamPoolId, but this project always scopes by
// compartment, never by pool.
func TestStreamList_UsesCompartmentIdNeverStreamPoolId(t *testing.T) {
	compartmentID := testCompartmentOCID
	var capturedReq streaming.ListStreamsRequest
	stub := &recordingStreamClient{
		onList: func(req streaming.ListStreamsRequest) (streaming.ListStreamsResponse, error) {
			capturedReq = req
			return streaming.ListStreamsResponse{}, nil
		},
	}

	if _, err := streamList(context.Background(), stub, compartmentID); err != nil {
		t.Fatalf("streamList() error = %v, want nil", err)
	}
	if capturedReq.CompartmentId == nil || *capturedReq.CompartmentId != compartmentID {
		t.Errorf("ListStreamsRequest.CompartmentId = %v, want %q", capturedReq.CompartmentId, compartmentID)
	}
	if capturedReq.StreamPoolId != nil {
		t.Errorf("ListStreamsRequest.StreamPoolId = %v, want nil", capturedReq.StreamPoolId)
	}
}

// recordingStreamClient implements streamClient, recording the exact request it received --
// used only to prove streamList's CompartmentId/StreamPoolId request-shaping decision.
type recordingStreamClient struct {
	onList func(req streaming.ListStreamsRequest) (streaming.ListStreamsResponse, error)
}

func (s *recordingStreamClient) ListStreams(
	_ context.Context,
	req streaming.ListStreamsRequest,
) (streaming.ListStreamsResponse, error) {
	return s.onList(req)
}

func (s *recordingStreamClient) DeleteStream(
	_ context.Context,
	_ streaming.DeleteStreamRequest,
) (streaming.DeleteStreamResponse, error) {
	return streaming.DeleteStreamResponse{}, nil
}

// TestStream_Filter is table-driven over every streaming.StreamSummaryLifecycleStateEnum value.
func TestStream_Filter(t *testing.T) {
	tests := []struct {
		state   streaming.StreamSummaryLifecycleStateEnum
		present bool
	}{
		{streaming.StreamSummaryLifecycleStateCreating, true},
		{streaming.StreamSummaryLifecycleStateActive, true},
		{streaming.StreamSummaryLifecycleStateUpdating, true},
		{streaming.StreamSummaryLifecycleStateDeleting, false},
		{streaming.StreamSummaryLifecycleStateDeleted, false},
		{streaming.StreamSummaryLifecycleStateFailed, false},
	}

	for _, tc := range tests {
		t.Run(string(tc.state), func(t *testing.T) {
			r := &Stream{}
			r.stream.LifecycleState = tc.state
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

// TestStream_Remove proves the SDK delete call fires with the right StreamId, against a client
// that never touches the network.
func TestStream_Remove(t *testing.T) {
	id := testResourceOCID
	stub := &stubStreamClient{}
	r := &Stream{client: stub}
	r.stream.Id = &id

	if err := r.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v, want nil", err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != id {
		t.Fatalf("stub.deleted = %v, want [%s]", stub.deleted, id)
	}
}
