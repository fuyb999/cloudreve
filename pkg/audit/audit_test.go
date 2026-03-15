package audit

import (
	"context"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
	"github.com/gofrs/uuid"
)

type testSettingStore struct {
	values map[string]any
}

func (s testSettingStore) Get(_ context.Context, name string, defaultVal any) any {
	if val, ok := s.values[name]; ok {
		return val
	}

	return defaultVal
}

type mockAuditLogClient struct {
	created []*inventory.CreateAuditLogArgs
}

func (m *mockAuditLogClient) SetClient(_ *ent.Client) inventory.TxOperator {
	return m
}

func (m *mockAuditLogClient) GetClient() *ent.Client {
	return nil
}

func (m *mockAuditLogClient) Create(_ context.Context, args *inventory.CreateAuditLogArgs) (*ent.AuditLog, error) {
	copyArgs := *args
	if args.Content != nil {
		copyArgs.Content = make(map[string]any, len(args.Content))
		for k, v := range args.Content {
			copyArgs.Content[k] = v
		}
	}

	m.created = append(m.created, &copyArgs)
	return &ent.AuditLog{ID: len(m.created)}, nil
}

func (m *mockAuditLogClient) List(context.Context, *inventory.ListAuditLogArgs) (*inventory.ListAuditLogResult, error) {
	return nil, nil
}

func TestManagerPublishPersistsAndNotifiesSubscribers(t *testing.T) {
	t.Parallel()

	client := &mockAuditLogClient{}
	provider := setting.NewProvider(testSettingStore{
		values: map[string]any{
			"audit_log_enabled_types": "[5]",
		},
	})
	manager := NewManager(client, provider, logging.NewConsoleLogger(logging.LevelError))
	t.Cleanup(manager.Close)

	subID, ch := manager.Subscribe(1)
	t.Cleanup(func() {
		manager.Unsubscribe(subID)
	})

	correlationID := uuid.Must(uuid.NewV4())
	ctx := context.WithValue(context.Background(), inventory.UserIDCtx{}, 7)
	ctx = context.WithValue(ctx, logging.CorrelationIDCtx{}, correlationID)

	if err := manager.Publish(ctx, &Event{
		Type:    UserLogin,
		Content: map[string]any{"status": "ok"},
	}); err != nil {
		t.Fatalf("publish failed: %v", err)
	}

	if len(client.created) != 1 {
		t.Fatalf("expected 1 persisted log, got %d", len(client.created))
	}

	created := client.created[0]
	if created.Type != UserLogin {
		t.Fatalf("unexpected type: got %d want %d", created.Type, UserLogin)
	}
	if created.UserID != 7 {
		t.Fatalf("unexpected user id: got %d want 7", created.UserID)
	}
	if created.CorrelationID != correlationID.String() {
		t.Fatalf("unexpected correlation id: got %s want %s", created.CorrelationID, correlationID.String())
	}

	select {
	case event := <-ch:
		if event.Type != UserLogin {
			t.Fatalf("unexpected subscriber event type: got %d want %d", event.Type, UserLogin)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscriber event")
	}
}

func TestManagerSkipsDisabledEvents(t *testing.T) {
	t.Parallel()

	client := &mockAuditLogClient{}
	provider := setting.NewProvider(testSettingStore{
		values: map[string]any{
			"audit_log_enabled_types": "[5]",
		},
	})
	manager := NewManager(client, provider, logging.NewConsoleLogger(logging.LevelError))
	t.Cleanup(manager.Close)

	if err := manager.Publish(context.Background(), &Event{Type: EmailSent}); err != nil {
		t.Fatalf("publish failed: %v", err)
	}

	if len(client.created) != 0 {
		t.Fatalf("expected no persisted logs, got %d", len(client.created))
	}
}
