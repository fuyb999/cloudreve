package indexer

import (
	"context"

	"github.com/cloudreve/Cloudreve/v4/pkg/searcher"
)

// NoopIndexer is a no-op implementation of SearchIndexer, used when FTS is disabled.
type NoopIndexer struct{}

func (n *NoopIndexer) UpsertFile(ctx context.Context, doc *searcher.SearchFileDocument) error {
	return nil
}

func (n *NoopIndexer) BulkUpsertFiles(ctx context.Context, docs []*searcher.SearchFileDocument) error {
	return nil
}

func (n *NoopIndexer) DeleteByFileIDs(ctx context.Context, fileID ...int) error {
	return nil
}

func (n *NoopIndexer) Search(ctx context.Context, req *searcher.SearchRequest) ([]searcher.SearchResult, int64, error) {
	return nil, 0, nil
}

func (n *NoopIndexer) IndexReady(ctx context.Context) (bool, error) {
	return true, nil
}

func (n *NoopIndexer) EnsureIndex(ctx context.Context) error {
	return nil
}

func (n *NoopIndexer) DeleteAll(ctx context.Context) error {
	return nil
}

func (n *NoopIndexer) Close() error {
	return nil
}
