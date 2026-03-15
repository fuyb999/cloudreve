package inventory

import (
	"context"
	"fmt"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/syncthingdevice"
	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
)

type (
	UpsertSyncthingDeviceArgs struct {
		UserID        int
		DeviceID      string
		ShortID       string
		LastIP        string
		APIKey        string
		JSONRaw       map[string]any
		BindURI       string
		ClientVersion string
		Platform      string
		LastSeenAt    time.Time
		Online        bool
	}

	SyncthingDeviceHeartbeatArgs struct {
		UserID     int
		DeviceID   string
		ShortID    string
		LastIP     string
		BindURI    string
		LastSeenAt time.Time
		Online     bool
	}

	SyncthingDeviceActivityArgs struct {
		UserID     int
		DeviceID   string
		ShortID    string
		LastIP     string
		BindURI    string
		LastSeenAt time.Time
		LastSyncAt time.Time
		Online     bool
	}

	SyncthingDeviceClient interface {
		TxOperator
		ListByUser(ctx context.Context, userID int) ([]*ent.SyncthingDevice, error)
		Upsert(ctx context.Context, args *UpsertSyncthingDeviceArgs) (*ent.SyncthingDevice, error)
		Heartbeat(ctx context.Context, args *SyncthingDeviceHeartbeatArgs) (*ent.SyncthingDevice, error)
		ReportActivity(ctx context.Context, args *SyncthingDeviceActivityArgs) (*ent.SyncthingDevice, error)
	}
)

func NewSyncthingDeviceClient(client *ent.Client, _ conf.DBType) SyncthingDeviceClient {
	return &syncthingDeviceClient{
		client: client,
	}
}

type syncthingDeviceClient struct {
	client *ent.Client
}

func (c *syncthingDeviceClient) SetClient(newClient *ent.Client) TxOperator {
	return &syncthingDeviceClient{client: newClient}
}

func (c *syncthingDeviceClient) GetClient() *ent.Client {
	return c.client
}

func (c *syncthingDeviceClient) ListByUser(ctx context.Context, userID int) ([]*ent.SyncthingDevice, error) {
	return c.client.SyncthingDevice.Query().
		Where(syncthingdevice.OwnerID(userID)).
		Order(
			syncthingdevice.ByOnline(sql.OrderDesc()),
			syncthingdevice.ByUpdatedAt(sql.OrderDesc()),
			syncthingdevice.ByID(sql.OrderDesc()),
		).
		All(ctx)
}

func (c *syncthingDeviceClient) Upsert(ctx context.Context, args *UpsertSyncthingDeviceArgs) (*ent.SyncthingDevice, error) {
	device, err := c.find(ctx, args.UserID, args.DeviceID)
	if err != nil {
		return nil, err
	}

	if device == nil {
		create := c.client.SyncthingDevice.Create().
			SetOwnerID(args.UserID).
			SetDeviceID(args.DeviceID).
			SetOnline(args.Online)
		if !args.LastSeenAt.IsZero() {
			create.SetLastSeenAt(args.LastSeenAt)
		}
		if args.ShortID != "" {
			create.SetShortID(args.ShortID)
		}
		if args.LastIP != "" {
			create.SetLastIP(args.LastIP)
		}
		if args.APIKey != "" {
			create.SetAPIKey(args.APIKey)
		}
		if args.JSONRaw != nil {
			create.SetJSONRaw(args.JSONRaw)
		}
		if args.BindURI != "" {
			create.SetBindURI(args.BindURI)
		}
		if args.ClientVersion != "" {
			create.SetClientVersion(args.ClientVersion)
		}
		if args.Platform != "" {
			create.SetPlatform(args.Platform)
		}
		res, createErr := create.Save(ctx)
		if createErr != nil {
			return nil, fmt.Errorf("failed to create syncthing device: %w", createErr)
		}
		return res, nil
	}

	update := c.client.SyncthingDevice.UpdateOneID(device.ID).
		SetOnline(args.Online)
	if !args.LastSeenAt.IsZero() {
		update.SetLastSeenAt(args.LastSeenAt)
	}
	if args.ShortID != "" {
		update.SetShortID(args.ShortID)
	}
	if args.LastIP != "" {
		update.SetLastIP(args.LastIP)
	}
	if args.APIKey != "" {
		update.SetAPIKey(args.APIKey)
	}
	if args.JSONRaw != nil {
		update.SetJSONRaw(args.JSONRaw)
	}
	if args.BindURI != "" {
		update.SetBindURI(args.BindURI)
	}
	if args.ClientVersion != "" {
		update.SetClientVersion(args.ClientVersion)
	}
	if args.Platform != "" {
		update.SetPlatform(args.Platform)
	}

	res, updateErr := update.Save(ctx)
	if updateErr != nil {
		return nil, fmt.Errorf("failed to update syncthing device: %w", updateErr)
	}
	return res, nil
}

func (c *syncthingDeviceClient) Heartbeat(ctx context.Context, args *SyncthingDeviceHeartbeatArgs) (*ent.SyncthingDevice, error) {
	device, err := c.find(ctx, args.UserID, args.DeviceID)
	if err != nil {
		return nil, err
	}

	if device == nil {
		create := c.client.SyncthingDevice.Create().
			SetOwnerID(args.UserID).
			SetDeviceID(args.DeviceID).
			SetOnline(args.Online)
		if args.ShortID != "" {
			create.SetShortID(args.ShortID)
		}
		if args.LastIP != "" {
			create.SetLastIP(args.LastIP)
		}
		if args.BindURI != "" {
			create.SetBindURI(args.BindURI)
		}
		if !args.LastSeenAt.IsZero() {
			create.SetLastSeenAt(args.LastSeenAt)
		}
		res, createErr := create.Save(ctx)
		if createErr != nil {
			return nil, fmt.Errorf("failed to create syncthing device heartbeat: %w", createErr)
		}
		return res, nil
	}

	update := c.client.SyncthingDevice.UpdateOneID(device.ID).
		SetOnline(args.Online)
	if args.ShortID != "" {
		update.SetShortID(args.ShortID)
	}
	if args.LastIP != "" {
		update.SetLastIP(args.LastIP)
	}
	if args.BindURI != "" {
		update.SetBindURI(args.BindURI)
	}
	if !args.LastSeenAt.IsZero() {
		update.SetLastSeenAt(args.LastSeenAt)
	}

	res, updateErr := update.Save(ctx)
	if updateErr != nil {
		return nil, fmt.Errorf("failed to update syncthing heartbeat: %w", updateErr)
	}
	return res, nil
}

func (c *syncthingDeviceClient) ReportActivity(ctx context.Context, args *SyncthingDeviceActivityArgs) (*ent.SyncthingDevice, error) {
	device, err := c.find(ctx, args.UserID, args.DeviceID)
	if err != nil {
		return nil, err
	}

	if device == nil {
		create := c.client.SyncthingDevice.Create().
			SetOwnerID(args.UserID).
			SetDeviceID(args.DeviceID).
			SetOnline(args.Online)
		if args.ShortID != "" {
			create.SetShortID(args.ShortID)
		}
		if args.LastIP != "" {
			create.SetLastIP(args.LastIP)
		}
		if args.BindURI != "" {
			create.SetBindURI(args.BindURI)
		}
		if !args.LastSeenAt.IsZero() {
			create.SetLastSeenAt(args.LastSeenAt)
		}
		if !args.LastSyncAt.IsZero() {
			create.SetLastSyncAt(args.LastSyncAt)
		}
		res, createErr := create.Save(ctx)
		if createErr != nil {
			return nil, fmt.Errorf("failed to create syncthing device activity: %w", createErr)
		}
		return res, nil
	}

	update := c.client.SyncthingDevice.UpdateOneID(device.ID).
		SetOnline(args.Online)
	if args.ShortID != "" {
		update.SetShortID(args.ShortID)
	}
	if args.LastIP != "" {
		update.SetLastIP(args.LastIP)
	}
	if args.BindURI != "" {
		update.SetBindURI(args.BindURI)
	}
	if !args.LastSeenAt.IsZero() {
		update.SetLastSeenAt(args.LastSeenAt)
	}
	if !args.LastSyncAt.IsZero() {
		update.SetLastSyncAt(args.LastSyncAt)
	}

	res, updateErr := update.Save(ctx)
	if updateErr != nil {
		return nil, fmt.Errorf("failed to update syncthing device activity: %w", updateErr)
	}
	return res, nil
}

func (c *syncthingDeviceClient) find(ctx context.Context, userID int, deviceID string) (*ent.SyncthingDevice, error) {
	res, err := c.client.SyncthingDevice.Query().
		Where(
			syncthingdevice.OwnerID(userID),
			syncthingdevice.DeviceID(deviceID),
		).
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query syncthing device: %w", err)
	}
	return res, nil
}
