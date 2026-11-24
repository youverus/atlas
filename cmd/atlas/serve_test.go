package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionFlagPrintsVersionWithoutConfig(t *testing.T) {
	t.Setenv("ATLAS_HOME", filepath.Join(t.TempDir(), "nonexistent"))
	opts, err := parseFlags([]string{"-version"})
	if err != nil {
		t.Fatalf("parseFlags(-version): %v", err)
	}
	var out, errBuf bytes.Buffer
	err = run(context.Background(), opts, streams{
		in:  strings.NewReader(""),
		out: &out,
		err: &errBuf,
	})
	if err != nil {
		t.Fatalf("run(-version): %v", err)
	}
	if strings.TrimSpace(out.String()) != version {
		t.Fatalf("version output = %q, want %q", out.String(), version)
	}
}

func TestServeFlagValidation(t *testing.T) {
	if _, err := parseFlags([]string{"-serve", "-rpc"}); err == nil {
		t.Fatal("expected error combining -serve and -rpc")
	}
	if _, err := parseFlags([]string{"-serve", "hello"}); err == nil {
		t.Fatal("expected error combining -serve and positional prompt")
	}
}

func TestStartServeHTTPWithBearerAndServeInfo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ATLAS_HOME", home)

	work := t.TempDir()
	if err := os.Mkdir(filepath.Join(work, "repo-a"), 0o755); err != nil {
		t.Fatalf("mkdir repo-a: %v", err)
	}
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{
		"defaultModel": "p1/m1",
		"providers": {
			"p1": {
				"api": "openai-completions",
				"apiKey": "k1",
				"models": [{"id": "m1", "name": "One", "contextWindow": 8000}]
			}
		}
	}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	infoPath := filepath.Join(home, "serve.json")
	opts, err := parseFlags([]string{
		"-serve",
		"-config", cfgPath,
		"-cwd", work,
		"-listen", "127.0.0.1:0",
		"-auth-token", "serve-secret-token",
		"-serve-info", infoPath,
	})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}

	var out, errBuf bytes.Buffer
	inst, err := startServeHTTP(context.Background(), opts, work, streams{
		in:  strings.NewReader(""),
		out: &out,
		err: &errBuf,
	})
	if err != nil {
		t.Fatalf("startServeHTTP: %v", err)
	}

	rawInfo, err := os.ReadFile(infoPath)
	if err != nil {
		inst.closeFunc()
		t.Fatalf("read serve-info: %v", err)
	}
	var meta serveInfoMetadata
	if err := json.Unmarshal(rawInfo, &meta); err != nil {
		inst.closeFunc()
		t.Fatalf("unmarshal serve-info: %v", err)
	}
	if meta.Addr != inst.addr || meta.Version != version || meta.PID != os.Getpid() {
		inst.closeFunc()
		t.Fatalf("serve-info = %+v, addr = %q", meta, inst.addr)
	}

	// Unauthenticated request is rejected.
	resp, err := http.Get(inst.info.URL + "/api/state")
	if err != nil {
		inst.closeFunc()
		t.Fatalf("unauth GET /api/state: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		inst.closeFunc()
		t.Fatalf("unauth status = %d, want 401", resp.StatusCode)
	}

	// Authenticated browse works on the served instance.
	req, _ := http.NewRequest(http.MethodGet, inst.info.URL+"/api/projects/browse?path="+work, nil)
	req.Header.Set("Authorization", "Bearer serve-secret-token")
	browseResp, err := http.DefaultClient.Do(req)
	if err != nil {
		inst.closeFunc()
		t.Fatalf("auth GET browse: %v", err)
	}
	defer browseResp.Body.Close()
	if browseResp.StatusCode != http.StatusOK {
		inst.closeFunc()
		t.Fatalf("auth browse status = %d, want 200", browseResp.StatusCode)
	}
	var browse struct {
		Entries []struct {
			Name string `json:"name"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(browseResp.Body).Decode(&browse); err != nil {
		inst.closeFunc()
		t.Fatalf("decode browse: %v", err)
	}
	if len(browse.Entries) != 1 || browse.Entries[0].Name != "repo-a" {
		inst.closeFunc()
		t.Fatalf("browse entries = %+v, want [repo-a]", browse.Entries)
	}

	inst.closeFunc()
	if _, err := os.Stat(infoPath); !os.IsNotExist(err) {
		t.Fatalf("serve-info should be removed after close, stat err = %v", err)
	}
}
