package inventory

import (
	"context"
	"errors"
	"fmt"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/syncthingdevice"
	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
)

type (
	UpsertSyncthingDeviceResult struct {
		Device              *ent.SyncthingDevice
		RestoreConfig       map[string]any
		RestoreFromDeviceID string
	}

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
		Upsert(ctx context.Context, args *UpsertSyncthingDeviceArgs) (*UpsertSyncthingDeviceResult, error)
		Heartbeat(ctx context.Context, args *SyncthingDeviceHeartbeatArgs) (*ent.SyncthingDevice, error)
		ReportActivity(ctx context.Context, args *SyncthingDeviceActivityArgs) (*ent.SyncthingDevice, error)
		Unbind(ctx context.Context, userID int, deviceID string) (*ent.SyncthingDevice, error)
	}
)

var (
	ErrSyncthingDeviceIPConflict    = errors.New("syncthing device ip conflict")
	ErrSyncthingDeviceNotRegistered = errors.New("syncthing device not registered")
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

func (c *syncthingDeviceClient) Upsert(ctx context.Context, args *UpsertSyncthingDeviceArgs) (*UpsertSyncthingDeviceResult, error) {
	device, err := c.find(ctx, args.UserID, args.DeviceID)
	if err != nil {
		return nil, err
	}

	if device != nil {
		if !device.IsBound {
			return nil, ErrSyncthingDeviceNotRegistered
		}

		res, updateErr := c.client.SyncthingDevice.UpdateOneID(device.ID).
			SetDeviceID(args.DeviceID).
			SetShortID(args.ShortID).
			SetLastIP(args.LastIP).
			SetAPIKey(args.APIKey).
			SetJSONRaw(args.JSONRaw).
			SetBindURI(args.BindURI).
			SetClientVersion(args.ClientVersion).
			SetPlatform(args.Platform).
			SetOnline(args.Online).
			SetIsBound(true).
			SetLastSeenAt(args.LastSeenAt).
			Save(ctx)
		if updateErr != nil {
			return nil, fmt.Errorf("failed to update syncthing device: %w", updateErr)
		}

		return &UpsertSyncthingDeviceResult{Device: res}, nil
	}

	conflict, err := c.findByIP(ctx, args.UserID, args.LastIP, true)
	if err != nil {
		return nil, err
	}
	if conflict != nil {
		return nil, ErrSyncthingDeviceIPConflict
	}

	candidate, err := c.findByIP(ctx, args.UserID, args.LastIP, false)
	if err != nil {
		return nil, err
	}
	if candidate != nil {
		res, updateErr := c.client.SyncthingDevice.UpdateOneID(candidate.ID).
			SetDeviceID(args.DeviceID).
			SetShortID(args.ShortID).
			SetLastIP(args.LastIP).
			SetAPIKey(args.APIKey).
			SetJSONRaw(args.JSONRaw).
			SetBindURI(args.BindURI).
			SetClientVersion(args.ClientVersion).
			SetPlatform(args.Platform).
			SetOnline(args.Online).
			SetIsBound(true).
			SetLastSeenAt(args.LastSeenAt).
			Save(ctx)
		if updateErr != nil {
			return nil, fmt.Errorf("failed to migrate syncthing device binding: %w", updateErr)
		}

		restoreConfig := candidate.JSONRaw
		if len(restoreConfig) == 0 {
			restoreConfig = nil
		}

		return &UpsertSyncthingDeviceResult{
			Device:              res,
			RestoreConfig:       restoreConfig,
			RestoreFromDeviceID: candidate.DeviceID,
		}, nil
	}

	create := c.client.SyncthingDevice.Create().
		SetOwnerID(args.UserID).
		SetDeviceID(args.DeviceID).
		SetShortID(args.ShortID).
		SetLastIP(args.LastIP).
		SetAPIKey(args.APIKey).
		SetJSONRaw(args.JSONRaw).
		SetBindURI(args.BindURI).
		SetClientVersion(args.ClientVersion).
		SetPlatform(args.Platform).
		SetOnline(args.Online).
		SetIsBound(true)
	if !args.LastSeenAt.IsZero() {
		create.SetLastSeenAt(args.LastSeenAt)
	}

	res, createErr := create.Save(ctx)
	if createErr != nil {
		return nil, fmt.Errorf("failed to create syncthing device: %w", createErr)
	}

	return &UpsertSyncthingDeviceResult{Device: res}, nil
}

func (c *syncthingDeviceClient) Heartbeat(ctx context.Context, args *SyncthingDeviceHeartbeatArgs) (*ent.SyncthingDevice, error) {
	device, err := c.find(ctx, args.UserID, args.DeviceID)
	if err != nil {
		return nil, err
	}
	if device == nil || !device.IsBound {
		return nil, ErrSyncthingDeviceNotRegistered
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
		return nil, fmt.Errorf("failed to update syncthing device activity: %w", updateErr)
	}
	return res, nil
}

func (c *syncthingDeviceClient) ReportActivity(ctx context.Context, args *SyncthingDeviceActivityArgs) (*ent.SyncthingDevice, error) {
	device, err := c.find(ctx, args.UserID, args.DeviceID)
	if err != nil {
		return nil, err
	}
	if device == nil || !device.IsBound {
		return nil, ErrSyncthingDeviceNotRegistered
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

func (c *syncthingDeviceClient) Unbind(ctx context.Context, userID int, deviceID string) (*ent.SyncthingDevice, error) {
	device, err := c.find(ctx, userID, deviceID)
	if err != nil {
		return nil, err
	}
	if device == nil {
		return nil, nil
	}

	res, updateErr := c.client.SyncthingDevice.UpdateOneID(device.ID).
		SetIsBound(false).
		SetOnline(false).
		Save(ctx)
	if updateErr != nil {
		return nil, fmt.Errorf("failed to unbind syncthing device: %w", updateErr)
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

func (c *syncthingDeviceClient) findByIP(ctx context.Context, userID int, lastIP string, isBound bool) (*ent.SyncthingDevice, error) {
	if lastIP == "" {
		return nil, nil
	}

	res, err := c.client.SyncthingDevice.Query().
		Where(
			syncthingdevice.OwnerID(userID),
			syncthingdevice.LastIP(lastIP),
			syncthingdevice.IsBound(isBound),
		).
		Order(
			syncthingdevice.ByUpdatedAt(sql.OrderDesc()),
			syncthingdevice.ByID(sql.OrderDesc()),
		).
		First(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query syncthing device by ip: %w", err)
	}
	return res, nil
}
