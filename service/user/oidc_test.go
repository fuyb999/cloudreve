package user

import "testing"

func TestNormalizeOIDCLoginEntryURL_RewritesYudaoAuthorizeEndpoint(t *testing.T) {
	discovery := &oidcDiscovery{
		Issuer:                "http://localhost:48080",
		AuthorizationEndpoint: "http://localhost:48080/admin-api/system/oauth2/authorize",
	}

	got := normalizeOIDCLoginEntryURL("http://localhost:48080/admin-api/system/oauth2/authorize", discovery)
	want := "http://localhost:48080/sso"
	if got != want {
		t.Fatalf("normalizeOIDCLoginEntryURL() = %q, want %q", got, want)
	}
}

func TestNormalizeOIDCLoginEntryURL_RewritesRootURLToYudaoSSO(t *testing.T) {
	discovery := &oidcDiscovery{
		Issuer:                "http://localhost:5173",
		AuthorizationEndpoint: "http://localhost:48080/admin-api/system/oauth2/authorize",
	}

	got := normalizeOIDCLoginEntryURL("http://localhost:5173", discovery)
	want := "http://localhost:5173/sso"
	if got != want {
		t.Fatalf("normalizeOIDCLoginEntryURL() = %q, want %q", got, want)
	}
}

func TestResolveOIDCLoginEntryURL_FallsBackToYudaoSSO(t *testing.T) {
	discovery := &oidcDiscovery{
		Issuer:                "http://localhost:5173",
		AuthorizationEndpoint: "http://localhost:48080/admin-api/system/oauth2/authorize",
	}

	got := resolveOIDCLoginEntryURL(nil, discovery)
	want := "http://localhost:5173/sso"
	if got != want {
		t.Fatalf("resolveOIDCLoginEntryURL() = %q, want %q", got, want)
	}
}

func TestResolveOIDCLocalUserID(t *testing.T) {
	got, err := resolveOIDCLocalUserID(&oidcIdentityProfile{ExternalUserID: "1001"})
	if err != nil {
		t.Fatalf("resolveOIDCLocalUserID() returned error: %v", err)
	}
	if got != 1001 {
		t.Fatalf("resolveOIDCLocalUserID() = %d, want 1001", got)
	}
}

func TestResolveOIDCLocalUserIDRejectsInvalidValue(t *testing.T) {
	if _, err := resolveOIDCLocalUserID(&oidcIdentityProfile{ExternalUserID: "admin"}); err == nil {
		t.Fatal("expected invalid external_user_id to be rejected")
	}
	if _, err := resolveOIDCLocalUserID(&oidcIdentityProfile{ExternalUserID: "0"}); err == nil {
		t.Fatal("expected non-positive external_user_id to be rejected")
	}
}
