package setting

import (
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/ent"
)

func TestIsSyncthingDeviceOnline(t *testing.T) {
	t.Parallel()

	now := time.Now()

	tests := []struct {
		name   string
		device *ent.SyncthingDevice
		want   bool
	}{
		{
			name: "online within window",
			device: &ent.SyncthingDevice{
				Online:     true,
				LastSeenAt: ptrTime(now.Add(-time.Minute)),
			},
			want: true,
		},
		{
			name: "offline when stale",
			device: &ent.SyncthingDevice{
				Online:     true,
				LastSeenAt: ptrTime(now.Add(-10 * time.Minute)),
			},
			want: false,
		},
		{
			name: "offline when flag cleared",
			device: &ent.SyncthingDevice{
				Online:     false,
				LastSeenAt: ptrTime(now.Add(-time.Minute)),
			},
			want: false,
		},
		{
			name: "offline when missing heartbeat",
			device: &ent.SyncthingDevice{
				Online: true,
			},
			want: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := isSyncthingDeviceOnline(tt.device, now); got != tt.want {
				t.Fatalf("unexpected online state: got %v want %v", got, tt.want)
			}
		})
	}
}

func ptrTime(v time.Time) *time.Time {
	return &v
}
