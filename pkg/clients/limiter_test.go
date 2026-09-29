package clients

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/identity"
)

// fakeInnerDispatcher increments an atomic counter on entry, sleeps briefly, decrements on
// exit, and records the observed maximum concurrent count -- the load-bearing, non-tautological
// proof that boundedDispatcher actually gates concurrency rather than merely existing. Remove
// the semaphore acquire/release in boundedDispatcher.Do and this test must fail.
type fakeInnerDispatcher struct {
	current int64
	max     int64
}

func (f *fakeInnerDispatcher) Do(_ *http.Request) (*http.Response, error) {
	n := atomic.AddInt64(&f.current, 1)
	for {
		old := atomic.LoadInt64(&f.max)
		if n <= old || atomic.CompareAndSwapInt64(&f.max, old, n) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	atomic.AddInt64(&f.current, -1)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
}

func TestBoundedDispatcher_NeverExceedsLimit(t *testing.T) {
	const limit = 2
	const goroutines = 10

	inner := &fakeInnerDispatcher{}
	d := &boundedDispatcher{inner: inner, sem: semaphore.NewWeighted(limit)}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.invalid", http.NoBody)
	if err != nil {
		t.Fatalf("unexpected error building request: %v", err)
	}

	done := make(chan struct{}, goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			resp, err := d.Do(req)
			if err != nil {
				t.Errorf("unexpected error from Do: %v", err)
				return
			}
			resp.Body.Close()
		}()
	}
	for i := 0; i < goroutines; i++ {
		<-done
	}

	if got := atomic.LoadInt64(&inner.max); got > limit {
		t.Fatalf("observed max concurrent calls %d exceeds configured limit %d", got, limit)
	}
}

// fakeConfigProvider implements common.ConfigurationProvider directly, mirroring
// pkg/ociauth/tenancy_test.go's pattern, so this test controls every field explicitly including
// a real RSA key the SDK's request signer needs before the request ever reaches HTTPClient.Do.
type fakeConfigProvider struct {
	key *rsa.PrivateKey
}

func (f *fakeConfigProvider) TenancyOCID() (string, error)    { return "ocid1.tenancy.oc1..test", nil }
func (f *fakeConfigProvider) UserOCID() (string, error)       { return "ocid1.user.oc1..test", nil }
func (f *fakeConfigProvider) KeyFingerprint() (string, error) { return "aa:bb:cc", nil }
func (f *fakeConfigProvider) Region() (string, error)         { return "us-ashburn-1", nil }
func (f *fakeConfigProvider) AuthType() (common.AuthConfig, error) {
	return common.AuthConfig{AuthType: common.UserPrincipal}, nil
}
func (f *fakeConfigProvider) KeyID() (string, error) {
	return "ocid1.tenancy.oc1..test/ocid1.user.oc1..test/aa:bb:cc", nil
}
func (f *fakeConfigProvider) PrivateRSAKey() (*rsa.PrivateKey, error) { return f.key, nil }

// countingStub returns statusCodes[call] in sequence (clamped to the last entry once exhausted),
// recording how many times Do was invoked -- proves the retry policy actually drove multiple
// attempts, not merely that it was constructed.
type countingStub struct {
	statusCodes []int
	calls       int
}

func (s *countingStub) Do(req *http.Request) (*http.Response, error) {
	idx := s.calls
	if idx >= len(s.statusCodes) {
		idx = len(s.statusCodes) - 1
	}
	code := s.statusCodes[idx]
	s.calls++
	body := `[]`
	if code != 200 {
		body = `{"code":"TooManyRequests","message":"too many requests"}`
	}
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

// TestCacheIdentity_RetryPolicyStillAttached proves this task's boundedDispatcher wrap does not
// silently replace or bypass Phase 1/2's already-attached retry policy: a stub dispatcher
// returning HTTP 429 for exactly two requests then 200, spliced in as boundedDispatcher's inner
// (the exact field this task's wrap point sets), still results in the real
// identity.IdentityClient.ListRegionSubscriptions call succeeding after retries -- proving
// common.DefaultRetryPolicy() (attached via SetCustomClientConfiguration, unchanged by this
// task) is still functioning through the wrap, not merely non-nil.
func TestCacheIdentity_RetryPolicyStillAttached(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	retry := common.DefaultRetryPolicy()
	cache := New(&fakeConfigProvider{key: key}, retry)

	client, err := cache.Identity("us-ashburn-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}

	bd, ok := client.HTTPClient.(*boundedDispatcher)
	if !ok {
		t.Fatalf("expected client.HTTPClient to be *boundedDispatcher, got %T", client.HTTPClient)
	}

	stub := &countingStub{statusCodes: []int{429, 429, 200}}
	bd.inner = stub

	tenancyID := "ocid1.tenancy.oc1..test"
	resp, err := client.ListRegionSubscriptions(context.Background(), identity.ListRegionSubscriptionsRequest{
		TenancyId: &tenancyID,
	})
	if err != nil {
		t.Fatalf("expected the retry policy to absorb two 429s and eventually succeed, got: %v", err)
	}
	if resp.RawResponse.StatusCode != 200 {
		t.Fatalf("expected final response status 200, got %d", resp.RawResponse.StatusCode)
	}
	if stub.calls != 3 {
		t.Fatalf("expected exactly 3 Do calls (2 retried 429s + 1 success), got %d", stub.calls)
	}
}
