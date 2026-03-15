package email

import (
	"context"

	"github.com/cloudreve/Cloudreve/v4/pkg/audit"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
)

type auditedDriver struct {
	next   Driver
	logger logging.Logger
}

func NewAuditedDriver(next Driver, logger logging.Logger) Driver {
	if next == nil {
		return nil
	}

	return &auditedDriver{
		next:   next,
		logger: logger,
	}
}

func (d *auditedDriver) Close() {
	d.next.Close()
}

func (d *auditedDriver) Send(ctx context.Context, to, title, body string) error {
	if err := d.next.Send(ctx, to, title, body); err != nil {
		return err
	}

	if err := audit.Publish(ctx, &audit.Event{
		Type: audit.EmailSent,
		Content: map[string]any{
			"email_to":    to,
			"email_title": title,
		},
	}); err != nil && d.logger != nil {
		d.logger.Warning("Failed to publish email audit log: %s", err)
	}

	return nil
}
