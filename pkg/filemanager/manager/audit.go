package manager

import (
	"context"

	"github.com/cloudreve/Cloudreve/v4/pkg/audit"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
)

func (m *manager) publishAudit(ctx context.Context, eventType int, content map[string]any, file fs.File, entity fs.Entity) {
	if m == nil || m.stateless {
		return
	}

	event := &audit.Event{
		Type:    eventType,
		Content: content,
	}
	if m.user != nil {
		event.UserID = m.user.ID
	}
	if file != nil && !file.IsNil() {
		event.FileID = file.ID()
	}
	if entity != nil {
		event.EntityID = entity.ID()
	}

	if err := audit.Publish(ctx, event); err != nil {
		m.l.Warning("Failed to publish audit event %d: %s", eventType, err)
	}
}

func (m *manager) getAuditFile(ctx context.Context, uri *fs.URI, opts ...fs.Option) fs.File {
	if m == nil || m.stateless || uri == nil {
		return nil
	}

	file, err := m.fs.Get(ctx, uri, opts...)
	if err != nil || file == nil || file.IsNil() {
		return nil
	}

	return file
}
