package clients

import (
	"crypto/rand"
	"crypto/rsa"
	"strings"
	"testing"

	"github.com/oracle/oci-go-sdk/v65/common"
)

// TestCacheCompute_CachesPerRegion proves Compute mirrors Identity's own caching contract: the
// same region returns the identical cached client instance on a second call, and a different
// region returns a distinct instance -- the first test for this file's caching behavior (no
// pre-existing cache_test.go covers even Identity's own caching yet).
func TestCacheCompute_CachesPerRegion(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cache := New(&fakeConfigProvider{key: key}, common.DefaultRetryPolicy())

	first, err := cache.Compute("us-ashburn-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	second, err := cache.Compute("us-ashburn-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	if first.HTTPClient != second.HTTPClient {
		t.Fatalf("expected the same cached client instance (same HTTPClient) for the same region")
	}

	other, err := cache.Compute("eu-frankfurt-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	if first.HTTPClient == other.HTTPClient {
		t.Fatalf("expected a different cached client instance for a different region")
	}
}

// TestCacheContainerEngine_CachesPerRegion proves ContainerEngine mirrors Compute's own caching
// contract (05-01-PLAN.md Task 1): the same region returns the identical cached client instance
// on a second call, and a different region returns a distinct instance.
func TestCacheContainerEngine_CachesPerRegion(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cache := New(&fakeConfigProvider{key: key}, common.DefaultRetryPolicy())

	first, err := cache.ContainerEngine("us-ashburn-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	second, err := cache.ContainerEngine("us-ashburn-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	if first.HTTPClient != second.HTTPClient {
		t.Fatalf("expected the same cached client instance (same HTTPClient) for the same region")
	}

	other, err := cache.ContainerEngine("eu-frankfurt-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	if first.HTTPClient == other.HTTPClient {
		t.Fatalf("expected a different cached client instance for a different region")
	}
}

// TestCacheArtifacts_CachesPerRegion proves Artifacts mirrors Compute's own caching contract
// (05-01-PLAN.md Task 1): the same region returns the identical cached client instance on a
// second call, and a different region returns a distinct instance.
func TestCacheArtifacts_CachesPerRegion(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cache := New(&fakeConfigProvider{key: key}, common.DefaultRetryPolicy())

	first, err := cache.Artifacts("us-ashburn-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	second, err := cache.Artifacts("us-ashburn-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	if first.HTTPClient != second.HTTPClient {
		t.Fatalf("expected the same cached client instance (same HTTPClient) for the same region")
	}

	other, err := cache.Artifacts("eu-frankfurt-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	if first.HTTPClient == other.HTTPClient {
		t.Fatalf("expected a different cached client instance for a different region")
	}
}

// TestCacheDatabase_CachesPerRegion proves Database mirrors Compute's own caching contract
// (05-02-PLAN.md Task 1): the same region returns the identical cached client instance on a
// second call, and a different region returns a distinct instance.
func TestCacheDatabase_CachesPerRegion(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cache := New(&fakeConfigProvider{key: key}, common.DefaultRetryPolicy())

	first, err := cache.Database("us-ashburn-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	second, err := cache.Database("us-ashburn-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	if first.HTTPClient != second.HTTPClient {
		t.Fatalf("expected the same cached client instance (same HTTPClient) for the same region")
	}

	other, err := cache.Database("eu-frankfurt-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	if first.HTTPClient == other.HTTPClient {
		t.Fatalf("expected a different cached client instance for a different region")
	}
}

// TestCacheMySQLDbSystem_CachesPerRegion proves MySQLDbSystem mirrors Compute's own caching
// contract (05-02-PLAN.md Task 1): the same region returns the identical cached client instance
// on a second call, and a different region returns a distinct instance.
func TestCacheMySQLDbSystem_CachesPerRegion(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cache := New(&fakeConfigProvider{key: key}, common.DefaultRetryPolicy())

	first, err := cache.MySQLDbSystem("us-ashburn-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	second, err := cache.MySQLDbSystem("us-ashburn-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	if first.HTTPClient != second.HTTPClient {
		t.Fatalf("expected the same cached client instance (same HTTPClient) for the same region")
	}

	other, err := cache.MySQLDbSystem("eu-frankfurt-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	if first.HTTPClient == other.HTTPClient {
		t.Fatalf("expected a different cached client instance for a different region")
	}
}

// TestCacheMySQLDbBackups_CachesPerRegion proves MySQLDbBackups mirrors Compute's own caching
// contract (05-02-PLAN.md Task 1): the same region returns the identical cached client instance
// on a second call, and a different region returns a distinct instance. Also proves MySQLDbSystem
// and MySQLDbBackups are distinct client instances, not aliases of one another, even though both
// live in the `mysql` package.
func TestCacheMySQLDbBackups_CachesPerRegion(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cache := New(&fakeConfigProvider{key: key}, common.DefaultRetryPolicy())

	first, err := cache.MySQLDbBackups("us-ashburn-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	second, err := cache.MySQLDbBackups("us-ashburn-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	if first.HTTPClient != second.HTTPClient {
		t.Fatalf("expected the same cached client instance (same HTTPClient) for the same region")
	}

	other, err := cache.MySQLDbBackups("eu-frankfurt-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	if first.HTTPClient == other.HTTPClient {
		t.Fatalf("expected a different cached client instance for a different region")
	}

	dbSystemClient, err := cache.MySQLDbSystem("us-ashburn-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	if dbSystemClient.HTTPClient == first.HTTPClient {
		t.Fatalf("MySQLDbSystem and MySQLDbBackups must be distinct clients, not aliases of one another")
	}
}

// TestCacheFunctionsManagement_CachesPerRegion proves FunctionsManagement mirrors Compute's own
// caching contract (05-03-PLAN.md Task 1): the same region returns the identical cached client
// instance on a second call, and a different region returns a distinct instance.
func TestCacheFunctionsManagement_CachesPerRegion(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cache := New(&fakeConfigProvider{key: key}, common.DefaultRetryPolicy())

	first, err := cache.FunctionsManagement("us-ashburn-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	second, err := cache.FunctionsManagement("us-ashburn-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	if first.HTTPClient != second.HTTPClient {
		t.Fatalf("expected the same cached client instance (same HTTPClient) for the same region")
	}

	other, err := cache.FunctionsManagement("eu-frankfurt-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	if first.HTTPClient == other.HTTPClient {
		t.Fatalf("expected a different cached client instance for a different region")
	}
}

// TestCacheStreamAdmin_CachesPerRegion proves StreamAdmin mirrors Compute's own caching contract
// (05-04-PLAN.md Task 1): the same region returns the identical cached client instance on a
// second call, and a different region returns a distinct instance.
func TestCacheStreamAdmin_CachesPerRegion(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cache := New(&fakeConfigProvider{key: key}, common.DefaultRetryPolicy())

	first, err := cache.StreamAdmin("us-ashburn-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	second, err := cache.StreamAdmin("us-ashburn-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	if first.HTTPClient != second.HTTPClient {
		t.Fatalf("expected the same cached client instance (same HTTPClient) for the same region")
	}

	other, err := cache.StreamAdmin("eu-frankfurt-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	if first.HTTPClient == other.HTTPClient {
		t.Fatalf("expected a different cached client instance for a different region")
	}
}

// TestCacheNotificationControlPlane_CachesPerRegion proves NotificationControlPlane mirrors
// Compute's own caching contract (05-04-PLAN.md Task 1): the same region returns the identical
// cached client instance on a second call, and a different region returns a distinct instance.
func TestCacheNotificationControlPlane_CachesPerRegion(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cache := New(&fakeConfigProvider{key: key}, common.DefaultRetryPolicy())

	first, err := cache.NotificationControlPlane("us-ashburn-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	second, err := cache.NotificationControlPlane("us-ashburn-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	if first.HTTPClient != second.HTTPClient {
		t.Fatalf("expected the same cached client instance (same HTTPClient) for the same region")
	}

	other, err := cache.NotificationControlPlane("eu-frankfurt-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	if first.HTTPClient == other.HTTPClient {
		t.Fatalf("expected a different cached client instance for a different region")
	}
}

// TestCacheNotificationDataPlane_CachesPerRegion proves NotificationDataPlane mirrors Compute's
// own caching contract (05-04-PLAN.md Task 1): the same region returns the identical cached
// client instance on a second call, and a different region returns a distinct instance. Also
// proves NotificationControlPlane and NotificationDataPlane are distinct client instances, not
// aliases of one another, even though both live in the `ons` package.
func TestCacheNotificationDataPlane_CachesPerRegion(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cache := New(&fakeConfigProvider{key: key}, common.DefaultRetryPolicy())

	first, err := cache.NotificationDataPlane("us-ashburn-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	second, err := cache.NotificationDataPlane("us-ashburn-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	if first.HTTPClient != second.HTTPClient {
		t.Fatalf("expected the same cached client instance (same HTTPClient) for the same region")
	}

	other, err := cache.NotificationDataPlane("eu-frankfurt-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	if first.HTTPClient == other.HTTPClient {
		t.Fatalf("expected a different cached client instance for a different region")
	}

	controlPlaneClient, err := cache.NotificationControlPlane("us-ashburn-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	if controlPlaneClient.HTTPClient == first.HTTPClient {
		t.Fatalf("NotificationControlPlane and NotificationDataPlane must be distinct clients, not aliases of one another")
	}
}

// TestCacheEvents_CachesPerRegion proves Events mirrors Compute's own caching contract
// (05-04-PLAN.md Task 1): the same region returns the identical cached client instance on a
// second call, and a different region returns a distinct instance.
func TestCacheEvents_CachesPerRegion(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cache := New(&fakeConfigProvider{key: key}, common.DefaultRetryPolicy())

	first, err := cache.Events("us-ashburn-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	second, err := cache.Events("us-ashburn-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	if first.HTTPClient != second.HTTPClient {
		t.Fatalf("expected the same cached client instance (same HTTPClient) for the same region")
	}

	other, err := cache.Events("eu-frankfurt-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	if first.HTTPClient == other.HTTPClient {
		t.Fatalf("expected a different cached client instance for a different region")
	}
}

// TestCacheKmsVault_CachesPerRegion proves KmsVault mirrors Compute's own caching contract
// (05-06-PLAN.md Task 1): the same region returns the identical cached client instance on a
// second call, and a different region returns a distinct instance -- KmsVault is a completely
// standard region-keyed accessor, unlike KmsManagement below.
func TestCacheKmsVault_CachesPerRegion(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cache := New(&fakeConfigProvider{key: key}, common.DefaultRetryPolicy())

	first, err := cache.KmsVault("us-ashburn-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	second, err := cache.KmsVault("us-ashburn-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	if first.HTTPClient != second.HTTPClient {
		t.Fatalf("expected the same cached client instance (same HTTPClient) for the same region")
	}

	other, err := cache.KmsVault("eu-frankfurt-1")
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	if first.HTTPClient == other.HTTPClient {
		t.Fatalf("expected a different cached client instance for a different region")
	}
}

// TestCacheKmsManagement_CachesPerManagementEndpoint proves KmsManagement is keyed by
// managementEndpoint, NOT region -- the codebase's first non-region-keyed Cache accessor
// (05-06-PLAN.md Task 1). The SAME endpoint string returns the identical cached client instance
// on a second call; two DIFFERENT endpoint strings (as two vaults in the same region would each
// have their own ManagementEndpoint) produce two distinct cached clients.
func TestCacheKmsManagement_CachesPerManagementEndpoint(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cache := New(&fakeConfigProvider{key: key}, common.DefaultRetryPolicy())

	const endpointA = "https://vault-a-kms.management.us-ashburn-1.oraclecloud.com"
	const endpointB = "https://vault-b-kms.management.us-ashburn-1.oraclecloud.com"

	first, err := cache.KmsManagement(endpointA)
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	second, err := cache.KmsManagement(endpointA)
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	if first.HTTPClient != second.HTTPClient {
		t.Fatalf("expected the same cached client instance (same HTTPClient) for the same managementEndpoint")
	}

	other, err := cache.KmsManagement(endpointB)
	if err != nil {
		t.Fatalf("unexpected error constructing client: %v", err)
	}
	if first.HTTPClient == other.HTTPClient {
		t.Fatalf("expected a different cached client instance for a different managementEndpoint, even though both vaults share the same region")
	}
}

// TestCacheAccessors_ConstructAndAttachRetryAndLimiter proves every accessor constructs without
// error and mirrors Identity's exact wrap shape: the retry policy attached and HTTPClient wrapped
// in *boundedDispatcher. Table-driven to keep cyclomatic complexity low while still exercising
// each accessor by name.
func TestCacheAccessors_ConstructAndAttachRetryAndLimiter(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cache := New(&fakeConfigProvider{key: key}, common.DefaultRetryPolicy())

	accessors := map[string]func(string) (common.HTTPRequestDispatcher, error){
		"Compute": func(r string) (common.HTTPRequestDispatcher, error) {
			c, err := cache.Compute(r)
			return c.HTTPClient, err
		},
		"ComputeManagement": func(r string) (common.HTTPRequestDispatcher, error) {
			c, err := cache.ComputeManagement(r)
			return c.HTTPClient, err
		},
		"Blockstorage": func(r string) (common.HTTPRequestDispatcher, error) {
			c, err := cache.Blockstorage(r)
			return c.HTTPClient, err
		},
		"VirtualNetwork": func(r string) (common.HTTPRequestDispatcher, error) {
			c, err := cache.VirtualNetwork(r)
			return c.HTTPClient, err
		},
		"ObjectStorage": func(r string) (common.HTTPRequestDispatcher, error) {
			c, err := cache.ObjectStorage(r)
			return c.HTTPClient, err
		},
		"LoadBalancer": func(r string) (common.HTTPRequestDispatcher, error) {
			c, err := cache.LoadBalancer(r)
			return c.HTTPClient, err
		},
		"NetworkLoadBalancer": func(r string) (common.HTTPRequestDispatcher, error) {
			c, err := cache.NetworkLoadBalancer(r)
			return c.HTTPClient, err
		},
		"FileStorage": func(r string) (common.HTTPRequestDispatcher, error) {
			c, err := cache.FileStorage(r)
			return c.HTTPClient, err
		},
		"ContainerEngine": func(r string) (common.HTTPRequestDispatcher, error) {
			c, err := cache.ContainerEngine(r)
			return c.HTTPClient, err
		},
		"Artifacts": func(r string) (common.HTTPRequestDispatcher, error) {
			c, err := cache.Artifacts(r)
			return c.HTTPClient, err
		},
		"Database": func(r string) (common.HTTPRequestDispatcher, error) {
			c, err := cache.Database(r)
			return c.HTTPClient, err
		},
		"MySQLDbSystem": func(r string) (common.HTTPRequestDispatcher, error) {
			c, err := cache.MySQLDbSystem(r)
			return c.HTTPClient, err
		},
		"MySQLDbBackups": func(r string) (common.HTTPRequestDispatcher, error) {
			c, err := cache.MySQLDbBackups(r)
			return c.HTTPClient, err
		},
		"FunctionsManagement": func(r string) (common.HTTPRequestDispatcher, error) {
			c, err := cache.FunctionsManagement(r)
			return c.HTTPClient, err
		},
		"StreamAdmin": func(r string) (common.HTTPRequestDispatcher, error) {
			c, err := cache.StreamAdmin(r)
			return c.HTTPClient, err
		},
		"NotificationControlPlane": func(r string) (common.HTTPRequestDispatcher, error) {
			c, err := cache.NotificationControlPlane(r)
			return c.HTTPClient, err
		},
		"NotificationDataPlane": func(r string) (common.HTTPRequestDispatcher, error) {
			c, err := cache.NotificationDataPlane(r)
			return c.HTTPClient, err
		},
		"Events": func(r string) (common.HTTPRequestDispatcher, error) {
			c, err := cache.Events(r)
			return c.HTTPClient, err
		},
		"KmsVault": func(r string) (common.HTTPRequestDispatcher, error) {
			c, err := cache.KmsVault(r)
			return c.HTTPClient, err
		},
		"KmsManagement": func(r string) (common.HTTPRequestDispatcher, error) {
			c, err := cache.KmsManagement(r)
			return c.HTTPClient, err
		},
	}

	seen := make(map[string]common.HTTPRequestDispatcher, len(accessors))
	for name, accessor := range accessors {
		httpClient, err := accessor("us-ashburn-1")
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", name, err)
		}
		if _, ok := httpClient.(*boundedDispatcher); !ok {
			t.Fatalf("%s: expected HTTPClient to be *boundedDispatcher, got %T", name, httpClient)
		}
		seen[name] = httpClient
	}

	if seen["Compute"] == seen["ComputeManagement"] {
		t.Fatalf("Compute and ComputeManagement must be distinct clients, not aliases of one another")
	}
}

// TestCacheAccessors_HonourRequestedRegion pins every region-scoped accessor to the region it was
// asked for, not the one the credentials happen to name. The distinction is invisible until the two
// differ: ObjectStorageClient overrides SetCustomClientConfiguration to call refreshRegion(), which
// re-derives the host from the configuration provider, so setting the region first and the retry
// policy second silently pinned every Object Storage read to the credentials' own region. A
// multi-region run then listed one region's buckets once per configured region -- reporting targets
// that do not exist where it says they do, and never seeing the ones that do.
//
// The accessor table above cannot catch this: it asks for us-ashburn-1 from a provider that already
// says us-ashburn-1. This one asks for something else on purpose.
func TestCacheAccessors_HonourRequestedRegion(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cache := New(&fakeConfigProvider{key: key}, common.DefaultRetryPolicy())

	// fakeConfigProvider reports us-ashburn-1, so ask for anything but that.
	const want = "eu-frankfurt-1"

	endpoints := map[string]func(string) (string, error){
		"Compute": func(r string) (string, error) {
			c, err := cache.Compute(r)
			return c.Endpoint(), err
		},
		"ComputeManagement": func(r string) (string, error) {
			c, err := cache.ComputeManagement(r)
			return c.Endpoint(), err
		},
		"Blockstorage": func(r string) (string, error) {
			c, err := cache.Blockstorage(r)
			return c.Endpoint(), err
		},
		"VirtualNetwork": func(r string) (string, error) {
			c, err := cache.VirtualNetwork(r)
			return c.Endpoint(), err
		},
		"ObjectStorage": func(r string) (string, error) {
			c, err := cache.ObjectStorage(r)
			return c.Endpoint(), err
		},
		"LoadBalancer": func(r string) (string, error) {
			c, err := cache.LoadBalancer(r)
			return c.Endpoint(), err
		},
		"NetworkLoadBalancer": func(r string) (string, error) {
			c, err := cache.NetworkLoadBalancer(r)
			return c.Endpoint(), err
		},
		"FileStorage": func(r string) (string, error) {
			c, err := cache.FileStorage(r)
			return c.Endpoint(), err
		},
		"ContainerEngine": func(r string) (string, error) {
			c, err := cache.ContainerEngine(r)
			return c.Endpoint(), err
		},
		"Artifacts": func(r string) (string, error) {
			c, err := cache.Artifacts(r)
			return c.Endpoint(), err
		},
		"Database": func(r string) (string, error) {
			c, err := cache.Database(r)
			return c.Endpoint(), err
		},
		"MySQLDbSystem": func(r string) (string, error) {
			c, err := cache.MySQLDbSystem(r)
			return c.Endpoint(), err
		},
		"MySQLDbBackups": func(r string) (string, error) {
			c, err := cache.MySQLDbBackups(r)
			return c.Endpoint(), err
		},
		"FunctionsManagement": func(r string) (string, error) {
			c, err := cache.FunctionsManagement(r)
			return c.Endpoint(), err
		},
		"StreamAdmin": func(r string) (string, error) {
			c, err := cache.StreamAdmin(r)
			return c.Endpoint(), err
		},
		"NotificationControlPlane": func(r string) (string, error) {
			c, err := cache.NotificationControlPlane(r)
			return c.Endpoint(), err
		},
		"NotificationDataPlane": func(r string) (string, error) {
			c, err := cache.NotificationDataPlane(r)
			return c.Endpoint(), err
		},
		"Events": func(r string) (string, error) {
			c, err := cache.Events(r)
			return c.Endpoint(), err
		},
		"KmsVault": func(r string) (string, error) {
			c, err := cache.KmsVault(r)
			return c.Endpoint(), err
		},
	}

	for name, accessor := range endpoints {
		endpoint, err := accessor(want)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", name, err)
		}
		if !strings.Contains(endpoint, want) {
			t.Errorf("%s: endpoint %q does not name the requested region %q -- the client will read the wrong region", name, endpoint, want)
		}
	}
}
