package remote

import (
	"os"
	"path/filepath"
	"testing"

	"pi-go/channel"
	"pi-go/internal/httpapi"
)

func TestManagerConfigPairingAllowlistAndBindings(t *testing.T) {
	dir := t.TempDir()
	channelsPath := filepath.Join(dir, "channels.json")
	dataDir := filepath.Join(dir, "channels")
	workDir := filepath.Join(dir, "work")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}

	mgr, err := NewManager(Options{
		ChannelsPath: channelsPath,
		DataDir:      dataDir,
		DefaultCWD:   workDir,
		Version:      "0.9.0",
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer func() { _ = mgr.Close() }()

	st, err := mgr.RemoteStatus()
	if err != nil {
		t.Fatalf("RemoteStatus initial: %v", err)
	}
	if st.State != "stopped" || st.Feishu.HasSecret {
		t.Fatalf("unexpected initial state: %+v", st)
	}

	// Reject nonexistent CWD.
	if _, err := mgr.SaveFeishuConfig(httpapi.FeishuConfigUpdate{
		AppID:       "cli_test",
		AppSecret:   "sec_12345678",
		DMPolicy:    "pairing",
		GroupPolicy: "allowlist",
		CWD:         filepath.Join(dir, "does-not-exist"),
	}); err == nil {
		t.Fatal("expected error for invalid CWD")
	}

	// Save valid config and ensure literal secret is masked, not echoed in plaintext.
	st, err = mgr.SaveFeishuConfig(httpapi.FeishuConfigUpdate{
		Enabled:        true,
		AppID:          "cli_test_app",
		AppSecret:      "sec_very_secret_9988",
		DMPolicy:       "pairing",
		GroupPolicy:    "open",
		RequireMention: true,
		AllowBots:      true,
		CWD:            workDir,
		Admins:         []string{"ou_admin1", "ou_admin1"},
		AllowedUsers:   []string{"ou_user1"},
	})
	if err != nil {
		t.Fatalf("SaveFeishuConfig: %v", err)
	}
	if !st.Feishu.Enabled || !st.Feishu.HasSecret || st.Feishu.SecretMode != "literal" {
		t.Fatalf("unexpected feishu view: %+v", st.Feishu)
	}
	if st.Feishu.SecretMasked == "sec_very_secret_9988" || st.Feishu.SecretMasked == "" {
		t.Fatalf("secret should be masked, got %q", st.Feishu.SecretMasked)
	}
	if len(st.Feishu.Admins) != 1 || st.Feishu.Admins[0] != "ou_admin1" {
		t.Fatalf("expected deduped admins, got %v", st.Feishu.Admins)
	}

	// Updating without sending AppSecret must preserve existing secret.
	st, err = mgr.SaveFeishuConfig(httpapi.FeishuConfigUpdate{
		Enabled:        false,
		AppID:          "cli_test_app",
		AppSecret:      "",
		DMPolicy:       "open",
		GroupPolicy:    "disabled",
		RequireMention: false,
		CWD:            workDir,
	})
	if err != nil {
		t.Fatalf("SaveFeishuConfig preserve secret: %v", err)
	}
	if !st.Feishu.HasSecret || st.Feishu.DMPolicy != "open" || st.Feishu.RequireMention {
		t.Fatalf("secret or fields not preserved: %+v", st.Feishu)
	}

	// Pairing approval workflow.
	pairing, err := channel.OpenPairing(filepath.Join(dataDir, "pairing.json"))
	if err != nil {
		t.Fatal(err)
	}
	code, err := pairing.NewCode("feishu", "ou_new_user", "oc_chat1")
	if err != nil {
		t.Fatal(err)
	}
	st, err = mgr.ApprovePairing(code)
	if err != nil {
		t.Fatalf("ApprovePairing: %v", err)
	}
	if len(st.PendingCodes) != 0 {
		t.Fatalf("expected pending code consumed, got %+v", st.PendingCodes)
	}
	if len(st.RuntimeAllowlist) != 1 || st.RuntimeAllowlist[0].UserID != "ou_new_user" {
		t.Fatalf("expected approved user in allowlist, got %+v", st.RuntimeAllowlist)
	}

	// Manual allow + revoke.
	st, err = mgr.AllowRemoteUser("feishu", "ou_manual")
	if err != nil {
		t.Fatalf("AllowRemoteUser: %v", err)
	}
	if len(st.RuntimeAllowlist) != 2 {
		t.Fatalf("expected 2 allowlist users, got %+v", st.RuntimeAllowlist)
	}
	st, err = mgr.RevokeRemoteUser("feishu", "ou_manual")
	if err != nil {
		t.Fatalf("RevokeRemoteUser: %v", err)
	}
	if len(st.RuntimeAllowlist) != 1 {
		t.Fatalf("expected 1 allowlist user after revoke, got %+v", st.RuntimeAllowlist)
	}

	// Clearing secret prevents StartRemote.
	st, err = mgr.SaveFeishuConfig(httpapi.FeishuConfigUpdate{
		AppID:       "cli_test_app",
		ClearSecret: true,
	})
	if err != nil {
		t.Fatalf("ClearSecret: %v", err)
	}
	if st.Feishu.HasSecret {
		t.Fatalf("expected secret cleared, got %+v", st.Feishu)
	}
	if _, err := mgr.StartRemote(); err == nil {
		t.Fatal("expected StartRemote to fail without secret")
	}
}
