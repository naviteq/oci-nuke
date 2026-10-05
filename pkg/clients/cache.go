package clients

import (
	"sync"

	"golang.org/x/sync/semaphore"

	"github.com/oracle/oci-go-sdk/v65/artifacts"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/containerengine"
	"github.com/oracle/oci-go-sdk/v65/core"
	"github.com/oracle/oci-go-sdk/v65/database"
	"github.com/oracle/oci-go-sdk/v65/events"
	"github.com/oracle/oci-go-sdk/v65/filestorage"
	"github.com/oracle/oci-go-sdk/v65/functions"
	"github.com/oracle/oci-go-sdk/v65/identity"
	"github.com/oracle/oci-go-sdk/v65/keymanagement"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/oracle/oci-go-sdk/v65/mysql"
	"github.com/oracle/oci-go-sdk/v65/networkloadbalancer"
	"github.com/oracle/oci-go-sdk/v65/objectstorage"
	"github.com/oracle/oci-go-sdk/v65/ons"
	"github.com/oracle/oci-go-sdk/v65/streaming"
	"github.com/oracle/oci-go-sdk/v65/vault"
)

// Cache is a region-keyed cache of OCI SDK service clients. OCI bakes the region into a
// client at construction time (ComputeClient.SetRegion / NewXClientWithConfigurationProvider
// reads provider.Region() once) -- unlike compartment, which is a per-request parameter --
// so clients are shared across every compartment-scoped scanner targeting the same region.
type Cache struct {
	provider common.ConfigurationProvider
	retry    common.RetryPolicy
	limiter  *semaphore.Weighted

	mu      sync.Mutex
	clients map[string]interface{} // key: "<service>/<region>"
}

// New constructs a Cache backed by provider, attaching retry to every client it builds and
// gating every client's outbound HTTP requests through one tenant-wide semaphore
// (DefaultMaxConcurrentRequests) -- see limiter.go.
func New(provider common.ConfigurationProvider, retry common.RetryPolicy) *Cache {
	return &Cache{
		provider: provider,
		retry:    retry,
		limiter:  semaphore.NewWeighted(int64(DefaultMaxConcurrentRequests)),
		clients:  make(map[string]interface{}),
	}
}

// Identity returns a cached identity.IdentityClient for region, constructing and caching
// one on first use. Every client built through this cache gets the shared retry policy --
// un-retried 429s become immediate queue failures otherwise.
func (c *Cache) Identity(region string) (identity.IdentityClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := "identity/" + region
	if existing, ok := c.clients[key]; ok {
		return existing.(identity.IdentityClient), nil
	}

	client, err := identity.NewIdentityClientWithConfigurationProvider(c.provider)
	if err != nil {
		return identity.IdentityClient{}, err
	}
	if region != "" {
		client.SetRegion(region)
	}
	client.SetCustomClientConfiguration(common.CustomClientConfiguration{RetryPolicy: &c.retry})
	client.HTTPClient = &boundedDispatcher{inner: client.HTTPClient, sem: c.limiter}

	c.clients[key] = client
	return client, nil
}

// Compute returns a cached core.ComputeClient for region, constructing and caching one on first
// use. Covers ListDedicatedVmHosts/DeleteDedicatedVmHost and
// ListComputeCapacityReservations/DeleteComputeCapacityReservation -- NOT instance pools or
// instance configurations, which live on ComputeManagement below (a different client).
func (c *Cache) Compute(region string) (core.ComputeClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := "compute/" + region
	if existing, ok := c.clients[key]; ok {
		return existing.(core.ComputeClient), nil
	}

	client, err := core.NewComputeClientWithConfigurationProvider(c.provider)
	if err != nil {
		return core.ComputeClient{}, err
	}
	if region != "" {
		client.SetRegion(region)
	}
	client.SetCustomClientConfiguration(common.CustomClientConfiguration{RetryPolicy: &c.retry})
	client.HTTPClient = &boundedDispatcher{inner: client.HTTPClient, sem: c.limiter}

	c.clients[key] = client
	return client, nil
}

// ComputeManagement returns a cached core.ComputeManagementClient for region, constructing and
// caching one on first use. Covers ListInstancePools/TerminateInstancePool and
// ListInstanceConfigurations/DeleteInstanceConfiguration -- a DIFFERENT client than Compute
// above; do not assume one client covers all compute resource types.
func (c *Cache) ComputeManagement(region string) (core.ComputeManagementClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := "computemanagement/" + region
	if existing, ok := c.clients[key]; ok {
		return existing.(core.ComputeManagementClient), nil
	}

	client, err := core.NewComputeManagementClientWithConfigurationProvider(c.provider)
	if err != nil {
		return core.ComputeManagementClient{}, err
	}
	if region != "" {
		client.SetRegion(region)
	}
	client.SetCustomClientConfiguration(common.CustomClientConfiguration{RetryPolicy: &c.retry})
	client.HTTPClient = &boundedDispatcher{inner: client.HTTPClient, sem: c.limiter}

	c.clients[key] = client
	return client, nil
}

// Blockstorage returns a cached core.BlockstorageClient for region, constructing and caching one
// on first use.
func (c *Cache) Blockstorage(region string) (core.BlockstorageClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := "blockstorage/" + region
	if existing, ok := c.clients[key]; ok {
		return existing.(core.BlockstorageClient), nil
	}

	client, err := core.NewBlockstorageClientWithConfigurationProvider(c.provider)
	if err != nil {
		return core.BlockstorageClient{}, err
	}
	if region != "" {
		client.SetRegion(region)
	}
	client.SetCustomClientConfiguration(common.CustomClientConfiguration{RetryPolicy: &c.retry})
	client.HTTPClient = &boundedDispatcher{inner: client.HTTPClient, sem: c.limiter}

	c.clients[key] = client
	return client, nil
}

// VirtualNetwork returns a cached core.VirtualNetworkClient for region, constructing and caching
// one on first use.
func (c *Cache) VirtualNetwork(region string) (core.VirtualNetworkClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := "virtualnetwork/" + region
	if existing, ok := c.clients[key]; ok {
		return existing.(core.VirtualNetworkClient), nil
	}

	client, err := core.NewVirtualNetworkClientWithConfigurationProvider(c.provider)
	if err != nil {
		return core.VirtualNetworkClient{}, err
	}
	if region != "" {
		client.SetRegion(region)
	}
	client.SetCustomClientConfiguration(common.CustomClientConfiguration{RetryPolicy: &c.retry})
	client.HTTPClient = &boundedDispatcher{inner: client.HTTPClient, sem: c.limiter}

	c.clients[key] = client
	return client, nil
}

// ObjectStorage returns a cached objectstorage.ObjectStorageClient for region, constructing and
// caching one on first use.
func (c *Cache) ObjectStorage(region string) (objectstorage.ObjectStorageClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := "objectstorage/" + region
	if existing, ok := c.clients[key]; ok {
		return existing.(objectstorage.ObjectStorageClient), nil
	}

	client, err := objectstorage.NewObjectStorageClientWithConfigurationProvider(c.provider)
	if err != nil {
		return objectstorage.ObjectStorageClient{}, err
	}
	// Order matters here and nowhere else in this file. ObjectStorageClient overrides
	// SetCustomClientConfiguration to call refreshRegion(), which re-derives the host
	// from the *configuration provider's* region -- silently undoing SetRegion. Calling
	// it second pinned every Object Storage read to the credentials' own region, so a
	// multi-region run scanned one region N times and never saw the others.
	client.SetCustomClientConfiguration(common.CustomClientConfiguration{RetryPolicy: &c.retry})
	if region != "" {
		client.SetRegion(region)
	}
	client.HTTPClient = &boundedDispatcher{inner: client.HTTPClient, sem: c.limiter}

	c.clients[key] = client
	return client, nil
}

// LoadBalancer returns a cached loadbalancer.LoadBalancerClient for region, constructing and
// caching one on first use.
func (c *Cache) LoadBalancer(region string) (loadbalancer.LoadBalancerClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := "loadbalancer/" + region
	if existing, ok := c.clients[key]; ok {
		return existing.(loadbalancer.LoadBalancerClient), nil
	}

	client, err := loadbalancer.NewLoadBalancerClientWithConfigurationProvider(c.provider)
	if err != nil {
		return loadbalancer.LoadBalancerClient{}, err
	}
	if region != "" {
		client.SetRegion(region)
	}
	client.SetCustomClientConfiguration(common.CustomClientConfiguration{RetryPolicy: &c.retry})
	client.HTTPClient = &boundedDispatcher{inner: client.HTTPClient, sem: c.limiter}

	c.clients[key] = client
	return client, nil
}

// NetworkLoadBalancer returns a cached networkloadbalancer.NetworkLoadBalancerClient for region,
// constructing and caching one on first use.
func (c *Cache) NetworkLoadBalancer(region string) (networkloadbalancer.NetworkLoadBalancerClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := "networkloadbalancer/" + region
	if existing, ok := c.clients[key]; ok {
		return existing.(networkloadbalancer.NetworkLoadBalancerClient), nil
	}

	client, err := networkloadbalancer.NewNetworkLoadBalancerClientWithConfigurationProvider(c.provider)
	if err != nil {
		return networkloadbalancer.NetworkLoadBalancerClient{}, err
	}
	if region != "" {
		client.SetRegion(region)
	}
	client.SetCustomClientConfiguration(common.CustomClientConfiguration{RetryPolicy: &c.retry})
	client.HTTPClient = &boundedDispatcher{inner: client.HTTPClient, sem: c.limiter}

	c.clients[key] = client
	return client, nil
}

// FileStorage returns a cached filestorage.FileStorageClient for region, constructing and
// caching one on first use.
func (c *Cache) FileStorage(region string) (filestorage.FileStorageClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := "filestorage/" + region
	if existing, ok := c.clients[key]; ok {
		return existing.(filestorage.FileStorageClient), nil
	}

	client, err := filestorage.NewFileStorageClientWithConfigurationProvider(c.provider)
	if err != nil {
		return filestorage.FileStorageClient{}, err
	}
	if region != "" {
		client.SetRegion(region)
	}
	client.SetCustomClientConfiguration(common.CustomClientConfiguration{RetryPolicy: &c.retry})
	client.HTTPClient = &boundedDispatcher{inner: client.HTTPClient, sem: c.limiter}

	c.clients[key] = client
	return client, nil
}

// ContainerEngine returns a cached containerengine.ContainerEngineClient for region,
// constructing and caching one on first use. Covers ListClusters/DeleteCluster and
// ListNodePools/DeleteNodePool -- both OKE resource types share this one client.
func (c *Cache) ContainerEngine(region string) (containerengine.ContainerEngineClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := "containerengine/" + region
	if existing, ok := c.clients[key]; ok {
		return existing.(containerengine.ContainerEngineClient), nil
	}

	client, err := containerengine.NewContainerEngineClientWithConfigurationProvider(c.provider)
	if err != nil {
		return containerengine.ContainerEngineClient{}, err
	}
	if region != "" {
		client.SetRegion(region)
	}
	client.SetCustomClientConfiguration(common.CustomClientConfiguration{RetryPolicy: &c.retry})
	client.HTTPClient = &boundedDispatcher{inner: client.HTTPClient, sem: c.limiter}

	c.clients[key] = client
	return client, nil
}

// Artifacts returns a cached artifacts.ArtifactsClient for region, constructing and caching one
// on first use. Covers ListContainerRepositories/DeleteContainerRepository -- OCIR's registry
// metadata API (not the container image push/pull data plane, which this cache does not touch).
func (c *Cache) Artifacts(region string) (artifacts.ArtifactsClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := "artifacts/" + region
	if existing, ok := c.clients[key]; ok {
		return existing.(artifacts.ArtifactsClient), nil
	}

	client, err := artifacts.NewArtifactsClientWithConfigurationProvider(c.provider)
	if err != nil {
		return artifacts.ArtifactsClient{}, err
	}
	if region != "" {
		client.SetRegion(region)
	}
	client.SetCustomClientConfiguration(common.CustomClientConfiguration{RetryPolicy: &c.retry})
	client.HTTPClient = &boundedDispatcher{inner: client.HTTPClient, sem: c.limiter}

	c.clients[key] = client
	return client, nil
}

// Database returns a cached database.DatabaseClient for region, constructing and caching one on
// first use. Covers ListAutonomousDatabases/DeleteAutonomousDatabase (AutonomousDatabase) and
// ListDbSystems/TerminateDbSystem (DbSystem, the Oracle `database` package's own DB System
// concept -- NOT MySQL; see MySQLDbSystem below for the distinct `mysql`-package client this
// wave's naming collision requires). Also covers the read-only backup enumerations
// ListAutonomousDatabaseBackups/ListBackups this wave's residue reporting uses
// (resources/database_backup_support.go) -- both are hosted on this SAME client, verified
// directly against oci-go-sdk/v65/database/database_client.go, so no fourth accessor is needed.
func (c *Cache) Database(region string) (database.DatabaseClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := "database/" + region
	if existing, ok := c.clients[key]; ok {
		return existing.(database.DatabaseClient), nil
	}

	client, err := database.NewDatabaseClientWithConfigurationProvider(c.provider)
	if err != nil {
		return database.DatabaseClient{}, err
	}
	if region != "" {
		client.SetRegion(region)
	}
	client.SetCustomClientConfiguration(common.CustomClientConfiguration{RetryPolicy: &c.retry})
	client.HTTPClient = &boundedDispatcher{inner: client.HTTPClient, sem: c.limiter}

	c.clients[key] = client
	return client, nil
}

// MySQLDbSystem returns a cached mysql.DbSystemClient for region, constructing and caching one on
// first use. Covers ListDbSystems/DeleteDbSystem for MySQL DB Systems -- a DIFFERENT client than
// Database above (the Oracle `database` package's own, unrelated DbSystemClient) and than
// MySQLDbBackups below (MySQL's OWN backup-operations client, a different client than this one
// despite both living in the `mysql` package).
func (c *Cache) MySQLDbSystem(region string) (mysql.DbSystemClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := "mysqldbsystem/" + region
	if existing, ok := c.clients[key]; ok {
		return existing.(mysql.DbSystemClient), nil
	}

	client, err := mysql.NewDbSystemClientWithConfigurationProvider(c.provider)
	if err != nil {
		return mysql.DbSystemClient{}, err
	}
	if region != "" {
		client.SetRegion(region)
	}
	client.SetCustomClientConfiguration(common.CustomClientConfiguration{RetryPolicy: &c.retry})
	client.HTTPClient = &boundedDispatcher{inner: client.HTTPClient, sem: c.limiter}

	c.clients[key] = client
	return client, nil
}

// MySQLDbBackups returns a cached mysql.DbBackupsClient for region, constructing and caching one
// on first use. Covers ListBackups for MySQL DB System automatic post-termination backups
// (read-only residue reporting only, resources/database_backup_support.go) --
// mysql.ListBackupsRequest is hosted on THIS client, NOT on MySQLDbSystem's mysql.DbSystemClient
// above, verified directly against oci-go-sdk/v65/mysql/mysql_dbbackups_client.go.
func (c *Cache) MySQLDbBackups(region string) (mysql.DbBackupsClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := "mysqldbbackups/" + region
	if existing, ok := c.clients[key]; ok {
		return existing.(mysql.DbBackupsClient), nil
	}

	client, err := mysql.NewDbBackupsClientWithConfigurationProvider(c.provider)
	if err != nil {
		return mysql.DbBackupsClient{}, err
	}
	if region != "" {
		client.SetRegion(region)
	}
	client.SetCustomClientConfiguration(common.CustomClientConfiguration{RetryPolicy: &c.retry})
	client.HTTPClient = &boundedDispatcher{inner: client.HTTPClient, sem: c.limiter}

	c.clients[key] = client
	return client, nil
}

// FunctionsManagement returns a cached functions.FunctionsManagementClient for region,
// constructing and caching one on first use. Covers ListApplications/DeleteApplication
// (Application) and ListFunctions/DeleteFunction (Function) -- both Functions resource types
// this plan registers share this ONE client, verified directly against
// oci-go-sdk/v65/functions/functions_functionsmanagement_client.go.
func (c *Cache) FunctionsManagement(region string) (functions.FunctionsManagementClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := "functionsmanagement/" + region
	if existing, ok := c.clients[key]; ok {
		return existing.(functions.FunctionsManagementClient), nil
	}

	client, err := functions.NewFunctionsManagementClientWithConfigurationProvider(c.provider)
	if err != nil {
		return functions.FunctionsManagementClient{}, err
	}
	if region != "" {
		client.SetRegion(region)
	}
	client.SetCustomClientConfiguration(common.CustomClientConfiguration{RetryPolicy: &c.retry})
	client.HTTPClient = &boundedDispatcher{inner: client.HTTPClient, sem: c.limiter}

	c.clients[key] = client
	return client, nil
}

// StreamAdmin returns a cached streaming.StreamAdminClient for region, constructing and caching
// one on first use. Covers ListStreams/DeleteStream (Stream) and
// ListStreamPools/DeleteStreamPool (StreamPool) -- the Streaming ADMIN plane, not
// streaming.StreamClient, the message-publish/consume data-plane client this project never needs.
func (c *Cache) StreamAdmin(region string) (streaming.StreamAdminClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := "streamadmin/" + region
	if existing, ok := c.clients[key]; ok {
		return existing.(streaming.StreamAdminClient), nil
	}

	client, err := streaming.NewStreamAdminClientWithConfigurationProvider(c.provider)
	if err != nil {
		return streaming.StreamAdminClient{}, err
	}
	if region != "" {
		client.SetRegion(region)
	}
	client.SetCustomClientConfiguration(common.CustomClientConfiguration{RetryPolicy: &c.retry})
	client.HTTPClient = &boundedDispatcher{inner: client.HTTPClient, sem: c.limiter}

	c.clients[key] = client
	return client, nil
}

// NotificationControlPlane returns a cached ons.NotificationControlPlaneClient for region,
// constructing and caching one on first use. Covers ListTopics/DeleteTopic (NotificationTopic)
// ONLY -- Topic operations live on this client, Subscription operations live on
// NotificationDataPlane below. The `ons` package ships two distinct clients; they are not
// interchangeable.
func (c *Cache) NotificationControlPlane(region string) (ons.NotificationControlPlaneClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := "notificationcontrolplane/" + region
	if existing, ok := c.clients[key]; ok {
		return existing.(ons.NotificationControlPlaneClient), nil
	}

	client, err := ons.NewNotificationControlPlaneClientWithConfigurationProvider(c.provider)
	if err != nil {
		return ons.NotificationControlPlaneClient{}, err
	}
	if region != "" {
		client.SetRegion(region)
	}
	client.SetCustomClientConfiguration(common.CustomClientConfiguration{RetryPolicy: &c.retry})
	client.HTTPClient = &boundedDispatcher{inner: client.HTTPClient, sem: c.limiter}

	c.clients[key] = client
	return client, nil
}

// NotificationDataPlane returns a cached ons.NotificationDataPlaneClient for region, constructing
// and caching one on first use. Covers ListSubscriptions/DeleteSubscription (Subscription) ONLY --
// Topic operations live on NotificationControlPlane above, NOT this client.
func (c *Cache) NotificationDataPlane(region string) (ons.NotificationDataPlaneClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := "notificationdataplane/" + region
	if existing, ok := c.clients[key]; ok {
		return existing.(ons.NotificationDataPlaneClient), nil
	}

	client, err := ons.NewNotificationDataPlaneClientWithConfigurationProvider(c.provider)
	if err != nil {
		return ons.NotificationDataPlaneClient{}, err
	}
	if region != "" {
		client.SetRegion(region)
	}
	client.SetCustomClientConfiguration(common.CustomClientConfiguration{RetryPolicy: &c.retry})
	client.HTTPClient = &boundedDispatcher{inner: client.HTTPClient, sem: c.limiter}

	c.clients[key] = client
	return client, nil
}

// Events returns a cached events.EventsClient for region, constructing and caching one on first
// use. Covers ListRules/DeleteRule (Rule).
func (c *Cache) Events(region string) (events.EventsClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := "events/" + region
	if existing, ok := c.clients[key]; ok {
		return existing.(events.EventsClient), nil
	}

	client, err := events.NewEventsClientWithConfigurationProvider(c.provider)
	if err != nil {
		return events.EventsClient{}, err
	}
	if region != "" {
		client.SetRegion(region)
	}
	client.SetCustomClientConfiguration(common.CustomClientConfiguration{RetryPolicy: &c.retry})
	client.HTTPClient = &boundedDispatcher{inner: client.HTTPClient, sem: c.limiter}

	c.clients[key] = client
	return client, nil
}

// KmsVault returns a cached keymanagement.KmsVaultClient for region, constructing and caching one
// on first use. Covers ListVaults/ScheduleVaultDeletion (Vault) -- vault-level, region-keyed
// operations only. KmsKey's per-vault key operations are addressed differently, see KmsManagement
// below.
func (c *Cache) KmsVault(region string) (keymanagement.KmsVaultClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := "kmsvault/" + region
	if existing, ok := c.clients[key]; ok {
		return existing.(keymanagement.KmsVaultClient), nil
	}

	client, err := keymanagement.NewKmsVaultClientWithConfigurationProvider(c.provider)
	if err != nil {
		return keymanagement.KmsVaultClient{}, err
	}
	if region != "" {
		client.SetRegion(region)
	}
	client.SetCustomClientConfiguration(common.CustomClientConfiguration{RetryPolicy: &c.retry})
	client.HTTPClient = &boundedDispatcher{inner: client.HTTPClient, sem: c.limiter}

	c.clients[key] = client
	return client, nil
}

// Vaults returns a cached vault.VaultsClient for region, constructing and caching one on first use.
// Covers ListSecrets/ScheduleSecretDeletion (VaultSecret). Secrets are the Secrets service's own
// API, region-keyed, not addressed through a vault's management endpoint the way keys are.
func (c *Cache) Vaults(region string) (vault.VaultsClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := "vaults/" + region
	if existing, ok := c.clients[key]; ok {
		return existing.(vault.VaultsClient), nil
	}

	client, err := vault.NewVaultsClientWithConfigurationProvider(c.provider)
	if err != nil {
		return vault.VaultsClient{}, err
	}
	if region != "" {
		client.SetRegion(region)
	}
	client.SetCustomClientConfiguration(common.CustomClientConfiguration{RetryPolicy: &c.retry})
	client.HTTPClient = &boundedDispatcher{inner: client.HTTPClient, sem: c.limiter}

	c.clients[key] = client
	return client, nil
}

// KmsManagement returns a cached keymanagement.KmsManagementClient for managementEndpoint,
// constructing and caching one on first use. Covers ListKeys/ScheduleKeyDeletion (KmsKey). This is
// the one deliberate deviation from every other Cache method's region-keyed cache map: KMS key
// operations are addressed per-vault, via each vault's own VaultSummary.ManagementEndpoint, not
// per-region -- OCI's NewKmsManagementClientWithConfigurationProvider constructor itself takes an
// endpoint string as its second argument (unlike every other client constructor in this file,
// which takes only the configuration provider and relies on a subsequent SetRegion call). Do NOT
// call client.SetRegion here -- the endpoint string already encodes the region, and there is no
// region parameter to set it from in the first place.
func (c *Cache) KmsManagement(managementEndpoint string) (keymanagement.KmsManagementClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := "kmsmanagement/" + managementEndpoint
	if existing, ok := c.clients[key]; ok {
		return existing.(keymanagement.KmsManagementClient), nil
	}

	client, err := keymanagement.NewKmsManagementClientWithConfigurationProvider(c.provider, managementEndpoint)
	if err != nil {
		return keymanagement.KmsManagementClient{}, err
	}
	client.SetCustomClientConfiguration(common.CustomClientConfiguration{RetryPolicy: &c.retry})
	client.HTTPClient = &boundedDispatcher{inner: client.HTTPClient, sem: c.limiter}

	c.clients[key] = client
	return client, nil
}
