package audit

import (
	"context"
	"sync"

	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/auth/requestinfo"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
)

type (
	Event struct {
		Type          int
		CorrelationID string
		IP            string
		Content       map[string]any
		UserID        int
		FileID        int
		EntityID      int
		ShareID       int
	}

	Manager interface {
		Publish(ctx context.Context, event *Event) error
		Subscribe(buffer int) (int, <-chan *Event)
		Unsubscribe(id int)
		Close()
	}

	manager struct {
		client      inventory.AuditLogClient
		settings    setting.Provider
		logger      logging.Logger
		mu          sync.RWMutex
		subscribers map[int]chan *Event
		nextID      int
		closed      bool
	}

	noopManager struct{}
)

var (
	defaultManager Manager = noopManager{}
	defaultMu      sync.RWMutex
)

func NewManager(client inventory.AuditLogClient, settings setting.Provider, logger logging.Logger) Manager {
	return &manager{
		client:      client,
		settings:    settings,
		logger:      logger,
		subscribers: map[int]chan *Event{},
	}
}

func SetDefault(m Manager) {
	defaultMu.Lock()
	defer defaultMu.Unlock()

	if defaultManager != nil {
		defaultManager.Close()
	}

	if m == nil {
		defaultManager = noopManager{}
		return
	}

	defaultManager = m
}

func Default() Manager {
	defaultMu.RLock()
	defer defaultMu.RUnlock()

	if defaultManager == nil {
		return noopManager{}
	}

	return defaultManager
}

func Publish(ctx context.Context, event *Event) error {
	return Default().Publish(ctx, event)
}

func Subscribe(buffer int) (int, <-chan *Event) {
	return Default().Subscribe(buffer)
}

func Unsubscribe(id int) {
	Default().Unsubscribe(id)
}

func CloseDefault() {
	SetDefault(nil)
}

func (m *manager) Publish(ctx context.Context, event *Event) error {
	if event == nil {
		return nil
	}

	e := normalizeEvent(ctx, event)
	if !m.settings.AuditLogEnabled(ctx, e.Type) {
		return nil
	}

	if _, err := m.client.Create(ctx, &inventory.CreateAuditLogArgs{
		Type:          e.Type,
		CorrelationID: e.CorrelationID,
		IP:            e.IP,
		Content:       e.Content,
		UserID:        e.UserID,
		FileID:        e.FileID,
		EntityID:      e.EntityID,
		ShareID:       e.ShareID,
	}); err != nil {
		return err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	for id, subscriber := range m.subscribers {
		select {
		case subscriber <- e:
		default:
			m.logger.Debug("Dropped audit event for slow subscriber %d.", id)
		}
	}

	return nil
}

func (m *manager) Subscribe(buffer int) (int, <-chan *Event) {
	if buffer <= 0 {
		buffer = 1
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		ch := make(chan *Event)
		close(ch)
		return 0, ch
	}

	m.nextID++
	id := m.nextID
	ch := make(chan *Event, buffer)
	m.subscribers[id] = ch
	return id, ch
}

func (m *manager) Unsubscribe(id int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	ch, ok := m.subscribers[id]
	if !ok {
		return
	}

	delete(m.subscribers, id)
	close(ch)
}

func (m *manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return
	}

	for id, ch := range m.subscribers {
		delete(m.subscribers, id)
		close(ch)
	}

	m.closed = true
}

func normalizeEvent(ctx context.Context, src *Event) *Event {
	res := &Event{
		Type:          src.Type,
		CorrelationID: src.CorrelationID,
		IP:            src.IP,
		Content:       cloneContent(src.Content),
		UserID:        src.UserID,
		FileID:        src.FileID,
		EntityID:      src.EntityID,
		ShareID:       src.ShareID,
	}

	if res.CorrelationID == "" {
		if cid := logging.CorrelationID(ctx); cid.String() != "" && cid.String() != "00000000-0000-0000-0000-000000000000" {
			res.CorrelationID = cid.String()
		}
	}

	if res.IP == "" {
		if reqInfo := requestinfo.RequestInfoFromContext(ctx); reqInfo != nil {
			res.IP = reqInfo.IP
			if res.Content == nil {
				res.Content = map[string]any{}
			}
			if reqInfo.UserAgent != "" {
				res.Content["user_agent"] = reqInfo.UserAgent
			}
		}
	}

	if res.UserID == 0 {
		res.UserID = inventory.UserIDFromContext(ctx)
	}

	return res
}

func cloneContent(content map[string]any) map[string]any {
	if content == nil {
		return nil
	}

	res := make(map[string]any, len(content))
	for k, v := range content {
		res[k] = v
	}

	return res
}

func (noopManager) Publish(context.Context, *Event) error {
	return nil
}

func (noopManager) Subscribe(int) (int, <-chan *Event) {
	ch := make(chan *Event)
	close(ch)
	return 0, ch
}

func (noopManager) Unsubscribe(int) {}

func (noopManager) Close() {}
