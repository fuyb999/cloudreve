package admin

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
)

func TestParseOptionalPositiveInt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    int
		wantErr bool
	}{
		{name: "empty", raw: "", want: 0},
		{name: "positive", raw: "42", want: 42},
		{name: "zero", raw: "0", wantErr: true},
		{name: "negative", raw: "-1", wantErr: true},
		{name: "invalid", raw: "abc", wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseOptionalPositiveInt(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("unexpected result: got %d want %d", got, tt.want)
			}
		})
	}
}

func TestBuildAuditLogResponseHidesInternalSystemUserEdge(t *testing.T) {
	t.Parallel()

	hasher, err := hashid.New("test-salt")
	if err != nil {
		t.Fatalf("failed to create hashid encoder: %v", err)
	}

	log := &ent.AuditLog{
		ID:     1,
		UserID: constants.PublicSystemOwnerID,
		Edges: ent.AuditLogEdges{
			User: &ent.User{
				ID:       constants.PublicSystemOwnerID,
				Email:    constants.PublicSystemOwnerEmail,
				Username: stringPtr(constants.PublicSystemOwnerUsername),
				Nick:     constants.PublicSystemOwnerNick,
			},
		},
	}

	res := buildAuditLogResponse(log, hasher)
	if res.UserHashID != "" {
		t.Fatalf("expected internal system user hash to be hidden, got %q", res.UserHashID)
	}
	if res.UserID != constants.PublicSystemOwnerID {
		t.Fatalf("expected internal system user id to be preserved, got %d", res.UserID)
	}
	if res.Edges.User != nil {
		t.Fatal("expected internal system user edge to be hidden")
	}
}

func TestBuildAuditLogResponseKeepsNormalUser(t *testing.T) {
	t.Parallel()

	hasher, err := hashid.New("test-salt")
	if err != nil {
		t.Fatalf("failed to create hashid encoder: %v", err)
	}

	log := &ent.AuditLog{
		ID:     2,
		UserID: 7,
		Edges: ent.AuditLogEdges{
			User: &ent.User{
				ID:       7,
				Email:    "normal@example.com",
				Username: stringPtr("normal-user"),
				Nick:     "normal",
			},
		},
	}

	res := buildAuditLogResponse(log, hasher)
	if res.UserHashID == "" {
		t.Fatal("expected normal user hash id to be preserved")
	}
	if res.UserID != 7 {
		t.Fatalf("expected normal user id to be preserved, got %+v", res)
	}
	if res.Edges.User == nil || res.Edges.User.ID != 7 {
		t.Fatal("expected normal user edge to be preserved")
	}
}

func TestBuildAuditLogResponseKeepsZeroTypeWhenMarshaled(t *testing.T) {
	t.Parallel()

	res := buildAuditLogResponse(&ent.AuditLog{
		ID:       3,
		Type:     0,
		UserID:   constants.PublicSystemOwnerID,
		FileID:   11,
		EntityID: 12,
		ShareID:  13,
	}, nil)

	payload, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("failed to marshal response: %v", err)
	}

	raw := string(payload)
	for _, expected := range []string{
		`"type":0`,
		`"user_id":-1`,
		`"file_id":11`,
		`"entity_id":12`,
		`"share_id":13`,
	} {
		if !strings.Contains(raw, expected) {
			t.Fatalf("expected marshaled response to contain %s, got %s", expected, raw)
		}
	}
}

func stringPtr(v string) *string {
	return &v
}
