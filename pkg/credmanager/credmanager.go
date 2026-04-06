package credmanager

import (
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/cloudreve/Cloudreve/v4/pkg/auth"
	"github.com/cloudreve/Cloudreve/v4/pkg/cache"
	"github.com/cloudreve/Cloudreve/v4/pkg/cluster"
	"github.com/cloudreve/Cloudreve/v4/pkg/cluster/routes"
	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/request"
)

type (
	// CredManager is a centralized for all Oauth tokens that requires periodic refresh
	// It is primarily used by OneDrive storage policy.
	CredManager interface {
		// Obtain gets a credential from the manager, refresh it if it's expired
		Obtain(ctx context.Context, key string) (Credential, error)
		// Upsert inserts or updates a credential in the manager
		Upsert(ctx context.Context, cred ...Credential) error
		RefreshAll(ctx context.Context)
	}

	Credential interface {
		String() string
		Refresh(ctx context.Context) (Credential, error)
		Key() string
		Expiry() time.Time
		RefreshedAt() *time.Time
	}
)

func init() {
	gob.Register(CredentialResponse{})
}

func New(kv cache.Driver) CredManager {
	return &credManager{
		kv:    kv,
		locks: make(map[string]*sync.Mutex),
	}
}

type (
	credManager struct {
		kv cache.Driver
		mu sync.RWMutex

		locks map[string]*sync.Mutex
	}
)

var (
	ErrNotFound = errors.New("credential not found")
)

func (m *credManager) Upsert(ctx context.Context, cred ...Credential) error {
	l := logging.FromContext(ctx)
	for _, c := range cred {
		l.Info("CredManager: Upsert credential for key %q...", c.Key())
		if err := m.kv.Set(c.Key(), c, 0); err != nil {
			return fmt.Errorf("failed to update credential in KV for key %q: %w", c.Key(), err)
		}

		m.lockForKey(c.Key())
	}

	return nil
}

func (m *credManager) Obtain(ctx context.Context, key string) (Credential, error) {
	l := logging.FromContext(ctx)
	lock := m.lockForKey(key)
	lock.Lock()
	defer lock.Unlock()

	item, err := credentialFromCache(m.kv, key)
	if err != nil {
		return nil, err
	}

	if item.Expiry().After(time.Now()) {
		// Credential is still valid
		return item, nil
	}

	// Credential is expired, refresh it
	l.Info("Refreshing credential for key %q...", key)
	newCred, err := item.Refresh(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh credential for key %q: %w", key, err)
	}

	l.Info("New credential for key %q is obtained, expire at %s", key, newCred.Expiry().String())
	if err := m.kv.Set(key, newCred, 0); err != nil {
		return nil, fmt.Errorf("failed to update credential in KV for key %q: %w", key, err)
	}

	return newCred, nil
}

func (m *credManager) RefreshAll(ctx context.Context) {
	l := logging.FromContext(ctx)
	for _, key := range m.keys() {
		func() {
			l.Info("Refreshing credential for key %q...", key)
			lock := m.lockForKey(key)
			lock.Lock()
			defer lock.Unlock()

			item, err := credentialFromCache(m.kv, key)
			if errors.Is(err, ErrNotFound) {
				l.Warning("Credential not found for key %q", key)
				return
			}
			if err != nil {
				l.Warning("Failed to read credential for key %q: %s", key, err)
				return
			}
			newCred, err := item.Refresh(ctx)
			if err != nil {
				l.Warning("Failed to refresh credential for key %q: %s", key, err)
				return
			}

			l.Info("New credential for key %q is obtained, expire at %s", key, newCred.Expiry().String())
			if err := m.kv.Set(key, newCred, 0); err != nil {
				l.Warning("Failed to update credential in KV for key %q: %s", key, err)
			}
		}()
	}
}

func (m *credManager) lockForKey(key string) *sync.Mutex {
	m.mu.RLock()
	lock, ok := m.locks[key]
	m.mu.RUnlock()
	if ok {
		return lock
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if lock, ok = m.locks[key]; !ok {
		lock = &sync.Mutex{}
		m.locks[key] = lock
	}

	return lock
}

func (m *credManager) keys() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	keys := make([]string, 0, len(m.locks))
	for key := range m.locks {
		keys = append(keys, key)
	}

	return keys
}

func credentialFromCache(kv cache.Driver, key string) (Credential, error) {
	itemRaw, ok := kv.Get(key)
	if !ok {
		return nil, fmt.Errorf("credential not found for key %q: %w", key, ErrNotFound)
	}

	item, ok := itemRaw.(Credential)
	if !ok {
		return nil, fmt.Errorf("invalid credential cache entry for key %q: %T", key, itemRaw)
	}

	return item, nil
}

type (
	slaveCredManager struct {
		kv     cache.Driver
		client request.Client
	}

	CredentialResponse struct {
		Token    string    `json:"token"`
		ExpireAt time.Time `json:"expire_at"`
	}
)

func NewSlaveManager(kv cache.Driver, config conf.ConfigProvider) CredManager {
	return &slaveCredManager{
		kv: kv,
		client: request.NewClient(
			config,
			request.WithCredential(auth.HMACAuth{
				SecretKey: []byte(config.Slave().Secret),
			}, int64(config.Slave().SignatureTTL)),
		),
	}
}

func (c CredentialResponse) String() string {
	return c.Token
}

func (c CredentialResponse) Refresh(ctx context.Context) (Credential, error) {
	return c, nil
}

func (c CredentialResponse) Key() string {
	return ""
}

func (c CredentialResponse) Expiry() time.Time {
	return c.ExpireAt
}

func (c CredentialResponse) RefreshedAt() *time.Time {
	return nil
}

func (m *slaveCredManager) Upsert(ctx context.Context, cred ...Credential) error {
	return nil
}

func (m *slaveCredManager) Obtain(ctx context.Context, key string) (Credential, error) {
	item, err := credentialFromCache(m.kv, key)
	if err == nil {
		return item, nil
	}

	if !errors.Is(err, ErrNotFound) {
		logging.FromContext(ctx).Warning("SlaveCredManager: Invalid cached credential for key %q: %s", key, err)
	}

	return m.requestCredFromMaster(ctx, key)
}

// No op on slave node
func (m *slaveCredManager) RefreshAll(ctx context.Context) {}

func (m *slaveCredManager) requestCredFromMaster(ctx context.Context, key string) (Credential, error) {
	l := logging.FromContext(ctx)
	l.Info("SlaveCredManager: Requesting credential for key %q from master...", key)

	requestDst := routes.MasterGetCredentialUrl(cluster.MasterSiteUrlFromContext(ctx), key)
	resp, err := m.client.Request(
		http.MethodGet,
		requestDst.String(),
		nil,
		request.WithContext(ctx),
		request.WithLogger(l),
		request.WithSlaveMeta(cluster.NodeIdFromContext(ctx)),
		request.WithCorrelationID(),
	).CheckHTTPResponse(http.StatusOK).DecodeResponse()
	if err != nil {
		return nil, fmt.Errorf("failed to request credential from master: %w", err)
	}

	cred := &CredentialResponse{}
	resp.GobDecode(&cred)

	if err := m.kv.Set(key, *cred, max(int(time.Until(cred.Expiry()).Seconds()), 1)); err != nil {
		return nil, fmt.Errorf("failed to update credential in KV for key %q: %w", key, err)
	}

	return cred, nil
}
