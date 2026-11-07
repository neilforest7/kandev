package execadmission

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func testRequest(t *testing.T) Request {
	t.Helper()
	profile, err := resolved().EffectiveProfile()
	require.NoError(t, err)
	return Request{
		OrganizationID: "22b11706-52e8-4aaa-98c5-c4eb735ddfd4",
		IssueID:        "d2855262-09ba-4c21-8a45-f2195af568ea",
		RootTaskID:     "root-task",
		WorkspaceID:    "78a71a86-99ae-4b9a-acf1-abeb07b4632d",
		TaskID:         "child-task",
		SessionID:      "session-1",
		Intent:         IntentImplementation,
		PlanVersion:    "",
		Profile:        profile,
	}
}

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(Config{BaseURL: server.URL, Token: "bridge-token"})
	require.NoError(t, err)
	return client, server
}

func TestCheckSendsExactBindingAndBearer(t *testing.T) {
	var received checkBody
	var authorization, contentType, method, path string
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		authorization, contentType, method, path = r.Header.Get("Authorization"), r.Header.Get("Content-Type"), r.Method, r.URL.Path
		require.NoError(t, json.NewDecoder(r.Body).Decode(&received))
		_, _ = w.Write([]byte(`{"approved":true,"reason":"ok","approvalId":"approval-1","planVersion":"plan-7"}`))
	})

	req := testRequest(t)
	result, err := client.Check(context.Background(), req)
	require.NoError(t, err)
	require.True(t, result.Approved)
	require.Equal(t, "approval-1", result.ApprovalID)
	require.Equal(t, "plan-7", result.PlanVersion)

	require.Equal(t, "Bearer bridge-token", authorization)
	require.Equal(t, "application/json", contentType)
	require.Equal(t, http.MethodPost, method)
	require.Equal(t, CheckPath, path)
	require.Equal(t, req.OrganizationID, received.OrganizationID)
	require.Equal(t, req.IssueID, received.IssueID)
	require.Equal(t, req.RootTaskID, received.RootTaskID)
	require.Equal(t, req.WorkspaceID, received.WorkspaceID)
	require.Equal(t, req.TaskID, received.TaskID)
	require.Equal(t, req.SessionID, received.SessionID)
	require.Equal(t, req.Intent, received.Intent)
	require.Equal(t, req.Profile, received.EffectiveProfile)
}

func TestCheckFailsClosed(t *testing.T) {
	req := testRequest(t)
	for name, handler := range map[string]http.HandlerFunc{
		"denied": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"approved":false,"reason":"revoked","approvalId":null,"planVersion":""}`))
		},
		"server error": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		},
		"not json": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("approved"))
		},
		"missing approved": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"approvalId":"approval-1","planVersion":"plan-7"}`))
		},
		"missing approval id": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"approved":true,"approvalId":"","planVersion":"plan-7"}`))
		},
		"missing plan version": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"approved":true,"approvalId":"approval-1","planVersion":""}`))
		},
		"plan version changed": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"approved":true,"approvalId":"approval-1","planVersion":"plan-8"}`))
		},
		"redirect": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", "http://198.51.100.7/internal/approvals/check")
			w.WriteHeader(http.StatusTemporaryRedirect)
		},
	} {
		client, _ := newTestClient(t, handler)
		pinned := req
		pinned.PlanVersion = "plan-7"
		if name != "plan version changed" {
			pinned.PlanVersion = ""
		}
		result, err := client.Check(context.Background(), pinned)
		require.Error(t, err, name)
		require.False(t, result.Approved, name)
	}

	deniedClient, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"approved":false,"reason":"paused","approvalId":null,"planVersion":""}`))
	})
	_, err := deniedClient.Check(context.Background(), req)
	var denied *Denied
	require.ErrorAs(t, err, &denied)
	require.Equal(t, "paused", denied.Reason)

	if _, err := (*Client)(nil).Check(context.Background(), req); err == nil {
		t.Fatal("a nil client must deny")
	}
}

func TestNewClientRequiresLoopbackAndToken(t *testing.T) {
	base := Config{BaseURL: "http://127.0.0.1:8787", Token: "token"}
	client, err := NewClient(base)
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:8787"+CheckPath, client.endpoint)

	_, err = NewClient(Config{BaseURL: "https://bridge.example.test", Token: "token"})
	require.Error(t, err)

	_, err = NewClient(Config{BaseURL: "http://127.0.0.1:8787"})
	require.Error(t, err)

	_, err = NewClient(Config{BaseURL: "not a url"})
	require.Error(t, err)
}

func TestLoadConfigReadsPrivateFileOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "execution-approval.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"baseUrl":"http://127.0.0.1:8787","token":"file-token"}`), 0o600))
	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:8787", cfg.BaseURL)
	require.Equal(t, "file-token", cfg.Token)

	_, err = LoadConfig(filepath.Join(t.TempDir(), "missing.json"))
	require.Error(t, err)

	unknown := filepath.Join(t.TempDir(), "unknown.json")
	require.NoError(t, os.WriteFile(unknown, []byte(`{"baseUrl":"http://127.0.0.1:8787","token":"t","extra":1}`), 0o600))
	_, err = LoadConfig(unknown)
	require.Error(t, err)
}

func TestCheckInvestigationProof(t *testing.T) {
	investigation := testRequest(t)
	investigation.Intent = IntentInvestigation
	require.Equal(t, "read-only", investigation.Profile.PermissionMode)

	client, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"approved":true,"reason":"ok","approvalId":null,"planVersion":"","basis":"investigation-proof"}`))
	})
	result, err := client.Check(context.Background(), investigation)
	require.NoError(t, err)
	require.True(t, result.Approved)
	require.Equal(t, BasisInvestigationProof, result.Basis)
	require.Empty(t, result.ApprovalID)
	require.Empty(t, result.PlanVersion)

	for name, handler := range map[string]http.HandlerFunc{
		"human plan approval is not an investigation proof": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"approved":true,"reason":"ok","approvalId":"approval-1","planVersion":"plan-7"}`))
		},
		"proof with approval id": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"approved":true,"reason":"ok","approvalId":"approval-1","planVersion":"","basis":"investigation-proof"}`))
		},
		"proof with plan version": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"approved":true,"reason":"ok","approvalId":null,"planVersion":"plan-7","basis":"investigation-proof"}`))
		},
		"proof without basis": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"approved":true,"reason":"ok","approvalId":null,"planVersion":""}`))
		},
	} {
		rejecting, _ := newTestClient(t, handler)
		_, err := rejecting.Check(context.Background(), investigation)
		require.Error(t, err, name)
	}

	pinned := investigation
	pinned.PlanVersion = "plan-7"
	accepted, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"approved":true,"reason":"ok","approvalId":null,"planVersion":"","basis":"investigation-proof"}`))
	})
	_, err = accepted.Check(context.Background(), pinned)
	require.Error(t, err, "an investigation never carries a pinned plan version")

	implementation := testRequest(t)
	implementation.Intent = IntentImplementation
	_, err = accepted.Check(context.Background(), implementation)
	require.Error(t, err, "an investigation proof cannot admit implementation")
}

func TestIntentForUsesPrivateProofAllowlist(t *testing.T) {
	readonly, err := resolved().EffectiveProfile()
	require.NoError(t, err)
	require.Equal(t, "read-only", readonly.PermissionMode)

	intent, err := IntentFor(readonly, nil)
	require.NoError(t, err)
	require.Equal(t, IntentImplementation, intent, "a read-only session is not investigation by mode alone")

	intent, err = IntentFor(readonly, []string{readonly.ID})
	require.NoError(t, err)
	require.Equal(t, IntentInvestigation, intent)

	intent, err = IntentFor(readonly, []string{"another-profile"})
	require.NoError(t, err)
	require.Equal(t, IntentImplementation, intent)

	writable := readonly
	writable.PermissionMode = "agent"
	_, err = IntentFor(writable, []string{writable.ID})
	require.Error(t, err, "a listed proof profile must still be read-only")
}

func TestConfigDiscoveryUsesThePrivateHomeChainOnly(t *testing.T) {
	home := t.TempDir()
	require.Empty(t, DiscoverConfigPath(home), "an absent file is not discovered")

	path := filepath.Join(home, ConfigFileName)
	require.NoError(t, os.WriteFile(path, []byte(`{"baseUrl":"http://127.0.0.1:8787","token":"t"}`), 0o600))
	require.Equal(t, path, DiscoverConfigPath(home))

	require.NoError(t, os.Chmod(path, 0o644))
	_, err := LoadConfig(path)
	require.Error(t, err, "group/world-readable config is refused")
	require.NoError(t, os.Chmod(path, 0o600))

	link := filepath.Join(t.TempDir(), "linked.json")
	require.NoError(t, os.Symlink(path, link))
	_, err = LoadConfig(link)
	require.Error(t, err, "a symlinked config is refused")

	require.Empty(t, DiscoverConfigPath(""))
}
