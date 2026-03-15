package setting

import (
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/auth/requestinfo"
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
	"github.com/gin-gonic/gin"
)

const syncthingDeviceOfflineAfter = 5 * time.Minute

type (
	ListSyncthingDevicesService  struct{}
	ListSyncthingDevicesParamCtx struct{}

	UpsertSyncthingDeviceService struct {
		DeviceID      string         `json:"device_id" binding:"required,min=1,max=255"`
		ShortID       string         `json:"short_id" binding:"required,min=1,max=64"`
		APIKey        string         `json:"api_key" binding:"required,min=1,max=255"`
		JSONRaw       map[string]any `json:"json_raw"`
		BindURI       string         `json:"bind_uri" binding:"omitempty,max=2048"`
		ClientVersion string         `json:"client_version" binding:"omitempty,max=255"`
		Platform      string         `json:"platform" binding:"omitempty,max=255"`
	}
	UpsertSyncthingDeviceParamCtx struct{}

	SyncthingHeartbeatService struct {
		DeviceID string `json:"device_id" binding:"required,min=1,max=255"`
		ShortID  string `json:"short_id" binding:"omitempty,max=64"`
		BindURI  string `json:"bind_uri" binding:"omitempty,max=2048"`
	}
	SyncthingHeartbeatParamCtx struct{}

	SyncthingActivityService struct {
		DeviceID string     `json:"device_id" binding:"required,min=1,max=255"`
		ShortID  string     `json:"short_id" binding:"omitempty,max=64"`
		BindURI  string     `json:"bind_uri" binding:"omitempty,max=2048"`
		SyncedAt *time.Time `json:"synced_at"`
	}
	SyncthingActivityParamCtx struct{}
)

func (service *ListSyncthingDevicesService) List(c *gin.Context) (*ListSyncthingDeviceResponse, error) {
	dep := dependency.FromContext(c)
	user := inventory.UserFromContext(c)

	devices, err := dep.SyncthingDeviceClient().ListByUser(c, user.ID)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeDBError, "Failed to list syncthing devices", err)
	}

	return BuildListSyncthingDeviceResponse(devices, time.Now()), nil
}

func (service *UpsertSyncthingDeviceService) Upsert(c *gin.Context) (*SyncthingDevice, error) {
	dep := dependency.FromContext(c)
	user := inventory.UserFromContext(c)
	now := time.Now()

	device, err := dep.SyncthingDeviceClient().Upsert(c, &inventory.UpsertSyncthingDeviceArgs{
		UserID:        user.ID,
		DeviceID:      service.DeviceID,
		ShortID:       service.ShortID,
		LastIP:        clientIPFromContext(c),
		APIKey:        service.APIKey,
		JSONRaw:       service.JSONRaw,
		BindURI:       service.BindURI,
		ClientVersion: service.ClientVersion,
		Platform:      service.Platform,
		LastSeenAt:    now,
		Online:        true,
	})
	if err != nil {
		return nil, serializer.NewError(serializer.CodeDBError, "Failed to save syncthing device", err)
	}

	resp := BuildSyncthingDevice(device, now)
	return &resp, nil
}

func (service *SyncthingHeartbeatService) Heartbeat(c *gin.Context) (*SyncthingDevice, error) {
	dep := dependency.FromContext(c)
	user := inventory.UserFromContext(c)
	now := time.Now()

	device, err := dep.SyncthingDeviceClient().Heartbeat(c, &inventory.SyncthingDeviceHeartbeatArgs{
		UserID:     user.ID,
		DeviceID:   service.DeviceID,
		ShortID:    service.ShortID,
		LastIP:     clientIPFromContext(c),
		BindURI:    service.BindURI,
		LastSeenAt: now,
		Online:     true,
	})
	if err != nil {
		return nil, serializer.NewError(serializer.CodeDBError, "Failed to update syncthing heartbeat", err)
	}

	resp := BuildSyncthingDevice(device, now)
	return &resp, nil
}

func (service *SyncthingActivityService) Report(c *gin.Context) (*SyncthingDevice, error) {
	dep := dependency.FromContext(c)
	user := inventory.UserFromContext(c)
	now := time.Now()
	lastSyncAt := now
	if service.SyncedAt != nil && !service.SyncedAt.IsZero() {
		lastSyncAt = service.SyncedAt.UTC()
	}

	device, err := dep.SyncthingDeviceClient().ReportActivity(c, &inventory.SyncthingDeviceActivityArgs{
		UserID:     user.ID,
		DeviceID:   service.DeviceID,
		ShortID:    service.ShortID,
		LastIP:     clientIPFromContext(c),
		BindURI:    service.BindURI,
		LastSeenAt: now,
		LastSyncAt: lastSyncAt,
		Online:     true,
	})
	if err != nil {
		return nil, serializer.NewError(serializer.CodeDBError, "Failed to update syncthing activity", err)
	}

	resp := BuildSyncthingDevice(device, now)
	return &resp, nil
}

func clientIPFromContext(c *gin.Context) string {
	if reqInfo := requestinfo.RequestInfoFromContext(c); reqInfo != nil {
		return reqInfo.IP
	}
	return ""
}

func isSyncthingDeviceOnline(device *ent.SyncthingDevice, now time.Time) bool {
	if device == nil || !device.Online || device.LastSeenAt == nil {
		return false
	}
	return device.LastSeenAt.After(now.Add(-syncthingDeviceOfflineAfter))
}
