package setting

import (
	"time"

	"github.com/cloudreve/Cloudreve/v4/ent"
)

type ListSyncthingDeviceResponse struct {
	Devices []SyncthingDevice `json:"devices"`
}

type SyncthingDevice struct {
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	DeviceID      string     `json:"device_id"`
	ShortID       string     `json:"short_id,omitempty"`
	LastIP        string     `json:"last_ip,omitempty"`
	BindURI       string     `json:"bind_uri,omitempty"`
	ClientVersion string     `json:"client_version,omitempty"`
	Platform      string     `json:"platform,omitempty"`
	LastSeenAt    *time.Time `json:"last_seen_at,omitempty"`
	LastSyncAt    *time.Time `json:"last_sync_at,omitempty"`
	Online        bool       `json:"online"`
}

func BuildListSyncthingDeviceResponse(devices []*ent.SyncthingDevice, now time.Time) *ListSyncthingDeviceResponse {
	res := make([]SyncthingDevice, 0, len(devices))
	for _, item := range devices {
		res = append(res, BuildSyncthingDevice(item, now))
	}

	return &ListSyncthingDeviceResponse{
		Devices: res,
	}
}

func BuildSyncthingDevice(device *ent.SyncthingDevice, now time.Time) SyncthingDevice {
	return SyncthingDevice{
		CreatedAt:     device.CreatedAt,
		UpdatedAt:     device.UpdatedAt,
		DeviceID:      device.DeviceID,
		ShortID:       device.ShortID,
		LastIP:        device.LastIP,
		BindURI:       device.BindURI,
		ClientVersion: device.ClientVersion,
		Platform:      device.Platform,
		LastSeenAt:    device.LastSeenAt,
		LastSyncAt:    device.LastSyncAt,
		Online:        isSyncthingDeviceOnline(device, now),
	}
}
