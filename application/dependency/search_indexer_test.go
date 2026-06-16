package dependency

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/searcher"
	searchindexer "github.com/cloudreve/Cloudreve/v4/pkg/searcher/indexer"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
)

type searchIndexerTestSettingStore struct {
	values map[string]any
}

func (s searchIndexerTestSettingStore) Get(ctx context.Context, name string, defaultVal any) any {
	if val, ok := s.values[name]; ok {
		return val
	}
	return defaultVal
}

func newSearchIndexerTestDep(endpoint string) *dependency {
	return NewDependency(
		WithLogger(logging.NewConsoleLogger(logging.LevelError)),
		WithSettingProvider(setting.NewProvider(searchIndexerTestSettingStore{
			values: map[string]any{
				"fts_enabled":                "1",
				"fts_index_type":             "elasticsearch",
				"fts_elasticsearch_endpoint": endpoint,
				"fts_elasticsearch_index":    "cloudreve_files",
			},
		})),
	).(*dependency)
}

func TestSearchIndexerRetriesAfterTransientElasticsearchFailure(t *testing.T) {
	dep := newSearchIndexerTestDep("http://127.0.0.1:1")

	idx := dep.SearchIndexer(context.Background())
	if !searchindexer.IsRetryableNoopIndexer(idx) {
		t.Fatalf("expected retryable noop after Elasticsearch failure, got %T (%v)", idx, searchindexer.UnavailableReason(idx))
	}
	if reason := searchindexer.UnavailableReason(idx); reason == "" {
		t.Fatal("expected unavailable reason to be recorded")
	}

	es := newSearchIndexerTestElasticsearchServer(t)
	defer es.Close()
	dep.settingProvider = setting.NewProvider(searchIndexerTestSettingStore{
		values: map[string]any{
			"fts_enabled":                "1",
			"fts_index_type":             "elasticsearch",
			"fts_elasticsearch_endpoint": es.URL,
			"fts_elasticsearch_index":    "cloudreve_files",
		},
	})
	dep.searchIndexerRetryAt = time.Now().Add(-time.Second)

	idx = dep.SearchIndexer(context.Background())
	if searchindexer.IsNoopIndexer(idx) {
		t.Fatalf("expected Elasticsearch indexer after retry, got noop: %s", searchindexer.UnavailableReason(idx))
	}
}

func TestSearchIndexerReloadKeepsPreviousActiveIndexerOnTransientFailure(t *testing.T) {
	es := newSearchIndexerTestElasticsearchServer(t)
	defer es.Close()
	dep := newSearchIndexerTestDep(es.URL)

	active := dep.SearchIndexer(context.Background())
	if searchindexer.IsNoopIndexer(active) {
		t.Fatalf("expected active Elasticsearch indexer, got noop: %s", searchindexer.UnavailableReason(active))
	}

	dep.settingProvider = setting.NewProvider(searchIndexerTestSettingStore{
		values: map[string]any{
			"fts_enabled":                "1",
			"fts_index_type":             "elasticsearch",
			"fts_elasticsearch_endpoint": "http://127.0.0.1:1",
			"fts_elasticsearch_index":    "cloudreve_files",
		},
	})
	reloaded := dep.SearchIndexer(context.WithValue(context.Background(), ReloadCtx{}, true))
	if reloaded != active {
		t.Fatalf("expected reload failure to keep previous active indexer, got %T want %T", reloaded, active)
	}
	if reason := dep.SearchIndexerUnavailableReason(); reason == "" {
		t.Fatal("expected reload failure reason to be recorded while previous active indexer is kept")
	}

	dep.settingProvider = setting.NewProvider(searchIndexerTestSettingStore{
		values: map[string]any{
			"fts_enabled":                "1",
			"fts_index_type":             "elasticsearch",
			"fts_elasticsearch_endpoint": es.URL,
			"fts_elasticsearch_index":    "cloudreve_files",
		},
	})
	dep.searchIndexerRetryAt = time.Now().Add(-time.Second)

	recovered := dep.SearchIndexer(context.Background())
	if searchindexer.IsNoopIndexer(recovered) {
		t.Fatalf("expected search indexer to recover after retry window, got noop: %s", searchindexer.UnavailableReason(recovered))
	}
	if reason := dep.SearchIndexerUnavailableReason(); reason != "" {
		t.Fatalf("expected reload failure reason to be cleared after recovery, got %q", reason)
	}
}

func TestSearchIndexerRuntimeTransientFailureTripsRetryableNoop(t *testing.T) {
	active := &runtimeFailureSearchIndexer{}
	dep := NewDependency(
		WithLogger(logging.NewConsoleLogger(logging.LevelError)),
		WithSearchIndexer(active),
	).(*dependency)

	idx := dep.SearchIndexer(context.Background())
	if searchindexer.IsNoopIndexer(idx) {
		t.Fatalf("expected active indexer, got noop: %s", searchindexer.UnavailableReason(idx))
	}

	active.err = searchindexer.RetryableUnavailableError(errors.New("status=503"))
	if err := idx.UpsertFile(context.Background(), &searcher.SearchFileDocument{ID: "1", FileID: 1}); !searchindexer.IsRetryableUnavailableError(err) {
		t.Fatalf("expected retryable unavailable error, got %v", err)
	}

	paused := dep.SearchIndexer(context.Background())
	if !searchindexer.IsRetryableNoopIndexer(paused) {
		t.Fatalf("expected runtime failure to trip retryable noop, got %T", paused)
	}
	if reason := searchindexer.UnavailableReason(paused); reason == "" {
		t.Fatal("expected runtime failure reason to be recorded")
	}
}

type runtimeFailureSearchIndexer struct {
	searcher.SearchIndexer
	err error
}

func (r *runtimeFailureSearchIndexer) UpsertFile(ctx context.Context, doc *searcher.SearchFileDocument) error {
	return r.err
}

func (r *runtimeFailureSearchIndexer) BulkUpsertFiles(ctx context.Context, docs []*searcher.SearchFileDocument) error {
	return r.err
}

func (r *runtimeFailureSearchIndexer) DeleteByFileIDs(ctx context.Context, fileID ...int) error {
	return r.err
}

func (r *runtimeFailureSearchIndexer) Search(ctx context.Context, req *searcher.SearchRequest) ([]searcher.SearchResult, int64, error) {
	return nil, 0, r.err
}

func (r *runtimeFailureSearchIndexer) IndexReady(ctx context.Context) (bool, error) {
	return true, r.err
}

func (r *runtimeFailureSearchIndexer) EnsureIndex(ctx context.Context) error {
	return r.err
}

func (r *runtimeFailureSearchIndexer) DeleteAll(ctx context.Context) error {
	return r.err
}

func (r *runtimeFailureSearchIndexer) Close() error {
	return nil
}

func newSearchIndexerTestElasticsearchServer(t *testing.T) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		switch {
		case r.Method == http.MethodHead && r.URL.Path == "/cloudreve_files":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPut && r.URL.Path == "/cloudreve_files/_mapping":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"acknowledged":true}`))
		default:
			t.Fatalf("unexpected Elasticsearch test request: %s %s", r.Method, r.URL.Path)
		}
	}))
}
