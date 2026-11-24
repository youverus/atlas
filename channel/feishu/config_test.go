package feishu

import (
	"encoding/json"
	"os"
	"testing"
)

func TestParseDefaults(t *testing.T) {
	raw := json.RawMessage(`{"appId":"cli_1","appSecret":"s"}`)
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DMPolicy != "pairing" || cfg.GroupPolicy != "allowlist" {
		t.Fatalf("defaults = %s/%s", cfg.DMPolicy, cfg.GroupPolicy)
	}
	if cfg.RequireMention == nil || !*cfg.RequireMention {
		t.Fatal("requireMention default must be true")
	}
}

func TestParseEnvOverrides(t *testing.T) {
	t.Setenv("ATLAS_FEISHU_APP_ID", "cli_env")
	t.Setenv("ATLAS_FEISHU_APP_SECRET", "sec_env")
	raw := json.RawMessage(`{"appId":"cli_file","appSecret":"sec_file"}`)
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AppID != "cli_env" || cfg.AppSecret != "sec_env" {
		t.Fatalf("env override lost: %s/%s", cfg.AppID, cfg.AppSecret)
	}
}

func TestParseSecretReference(t *testing.T) {
	t.Setenv("MY_SECRET", "real")
	cfg, err := Parse(json.RawMessage(`{"appId":"a","appSecret":"env:MY_SECRET"}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AppSecret != "real" {
		t.Fatalf("env ref unresolved: %q", cfg.AppSecret)
	}
	if _, err := Parse(json.RawMessage(`{"appId":"a","appSecret":"env:MISSING_VAR"}`)); err == nil {
		t.Fatal("missing env var must fail")
	}
}

func TestParseRequiresCredentials(t *testing.T) {
	dir := t.TempDir()
	os.Unsetenv("ATLAS_FEISHU_APP_ID")
	os.Unsetenv("ATLAS_FEISHU_APP_SECRET")
	if _, err := Parse(json.RawMessage(`{}`)); err == nil {
		t.Fatal("empty config must fail")
	}
	_ = dir
}
