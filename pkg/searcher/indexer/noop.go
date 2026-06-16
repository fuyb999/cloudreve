package indexer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cloudreve/Cloudreve/v4/pkg/searcher"
	"github.com/meilisearch/meilisearch-go"
)

const RetryableUnavailableDelay = 30 * time.Second

var ErrRetryableUnavailable = errors.New("search indexer is temporarily unavailable")

type retryableUnavailableError struct {
	err error
}

func (e *retryableUnavailableError) Error() string {
	if e == nil || e.err == nil {
		return ErrRetryableUnavailable.Error()
	}

	return fmt.Sprintf("%s: %s", ErrRetryableUnavailable, e.err)
}

func (e *retryableUnavailableError) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.err
}

func (e *retryableUnavailableError) Is(target error) bool {
	return target == ErrRetryableUnavailable
}

// NoopIndexer is a no-op implementation of SearchIndexer, used when FTS is disabled.
type NoopIndexer struct {
	reason    string
	retryable bool
}

// NewNoopIndexer returns a disabled SearchIndexer with an operator-facing reason.
func NewNoopIndexer(reason string) *NoopIndexer {
	return &NoopIndexer{reason: reason}
}

// NewRetryableNoopIndexer returns a disabled SearchIndexer for transient initialization failures.
func NewRetryableNoopIndexer(reason string) *NoopIndexer {
	return &NoopIndexer{reason: reason, retryable: true}
}

func IsNoopIndexer(idx searcher.SearchIndexer) bool {
	if idx == nil {
		return true
	}

	_, ok := idx.(*NoopIndexer)
	return ok
}

// IsRetryableNoopIndexer reports whether a disabled indexer should be retried automatically.
func IsRetryableNoopIndexer(idx searcher.SearchIndexer) bool {
	noop, ok := idx.(*NoopIndexer)
	return ok && noop.retryable
}

// UnavailableReason returns the concrete reason a SearchIndexer is unavailable.
func UnavailableReason(idx searcher.SearchIndexer) string {
	if idx == nil {
		return "search indexer dependency is nil"
	}

	noop, ok := idx.(*NoopIndexer)
	if !ok {
		return ""
	}

	if noop.reason == "" {
		return "full-text search indexer is disabled or not configured"
	}

	return noop.reason
}

// UnavailableError returns a stable task error with the operator-facing reason attached.
func UnavailableError(idx searcher.SearchIndexer) error {
	reason := UnavailableReason(idx)
	if reason == "" {
		return fmt.Errorf("search indexer is unavailable")
	}

	return fmt.Errorf("search indexer is unavailable: %s", reason)
}

func RetryableUnavailableError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrRetryableUnavailable) {
		return err
	}

	return &retryableUnavailableError{err: err}
}

func IsRetryableUnavailableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrRetryableUnavailable) {
		return true
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	var meiliErr *meilisearch.Error
	if errors.As(err, &meiliErr) {
		if isRetryableHTTPStatus(meiliErr.StatusCode) {
			return true
		}
		switch meiliErr.ErrCode {
		case meilisearch.MeilisearchTimeoutError,
			meilisearch.MeilisearchCommunicationError,
			meilisearch.MeilisearchMaxRetriesExceeded:
			return true
		}
	}

	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"connection refused",
		"connection reset",
		"connection reset by peer",
		"connection timed out",
		"no such host",
		"server misbehaving",
		"temporary failure in name resolution",
		"i/o timeout",
		"broken pipe",
		"unexpected eof",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}

	for _, marker := range []string{
		"status=429",
		"status=502",
		"status=503",
		"status=504",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}

	return false
}

func RetryableUnavailableIfTransient(err error) error {
	if IsRetryableUnavailableError(err) {
		return RetryableUnavailableError(err)
	}

	return err
}

func IsRetryableStatus(status string) bool {
	code, err := strconv.Atoi(strings.TrimSpace(status))
	return err == nil && isRetryableHTTPStatus(code)
}

func isRetryableHTTPStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

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
