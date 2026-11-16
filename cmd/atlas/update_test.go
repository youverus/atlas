package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"pi-go/internal/tui"
)

// updateAssetName is the archive a release carries for this platform.
func updateAssetName() string {
	return "atlas-" + runtime.GOOS + "-" + runtime.GOARCH + ".tar.gz"
}

// updateTar builds the tar.gz one release asset holds: a single entry named
// like the binary, with the given contents.
func updateTar(t *testing.T, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: strings.TrimSuffix(updateAssetName(), ".tar.gz"), Size: int64(len(content)), Mode: 0o755}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatalf("tar body: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// updateSums writes the SHA256SUMS line for the archive this platform uses.
func updateSums(t *testing.T, archive []byte) string {
	t.Helper()
	digest := sha256.Sum256(archive)
	return fmt.Sprintf("%s  %s\n", hex.EncodeToString(digest[:]), updateAssetName())
}

// rewriteTransport sends every request to one httptest server, keeping the
// path. The client under test builds github.com URLs from a repo name; the
// transport is what lets a test answer them without a network.
type rewriteTransport struct {
	target string
}

func (r rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = "http"
	clone.URL.Host = r.target
	return http.DefaultTransport.RoundTrip(clone)
}

// updateServer serves the release redirect, the archive, and the checksums
// from one mux, the way github.com's web layer would. It returns the client
// whose transport rewrites to it.
func updateServer(t *testing.T, tag string, archive []byte, sums string) (*http.Client, string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/test/atlas/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://github.com/test/atlas/releases/tag/"+tag, http.StatusFound)
	})
	mux.HandleFunc("/test/atlas/releases/download/"+tag+"/"+updateAssetName(), func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	})
	mux.HandleFunc("/test/atlas/releases/download/"+tag+"/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, sums)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return &http.Client{Transport: rewriteTransport{target: strings.TrimPrefix(server.URL, "http://")}}, server.URL
}

// TestCompareVersions: the ordering a release check depends on.
func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.12.22", "0.12.22", 0},
		{"0.12.23", "0.12.22", 1},
		{"0.12.22", "0.12.23", -1},
		{"0.13.0", "0.12.22", 1},
		{"1.0.0", "0.99.9", 1},
		{"0.12.3", "0.12.22", -1},
		// A part that is not a number falls back to string order, so a
		// malformed version can never equal a real one.
		{"0.12.x", "0.12.22", 1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// TestParseAtlasVersion: the prefix is what tells an atlas tag from any other
// tag in the repository.
func TestParseAtlasVersion(t *testing.T) {
	if got, err := parseAtlasVersion("atlas-v0.12.23"); err != nil || got != "0.12.23" {
		t.Fatalf("parseAtlasVersion = %q, %v; want 0.12.23", got, err)
	}
	for _, tag := range []string{"v0.12.23", "atlas-0.12.23", "", "atlas-v", "atlas-v0.12.x"} {
		if _, err := parseAtlasVersion(tag); err == nil {
			t.Errorf("parseAtlasVersion(%q) succeeded, want an error", tag)
		}
	}
}

// TestCheckUpdateReportsOnlyNewer: the check is the gate the startup flash
// reads, and it must answer the current build with silence.
func TestCheckUpdateReportsOnlyNewer(t *testing.T) {
	archive := updateTar(t, "new")
	client, _ := updateServer(t, "atlas-v99.0.0", archive, updateSums(t, archive))
	if newest, err := checkUpdate(t.Context(), client, "test/atlas"); err != nil || newest != "99.0.0" {
		t.Fatalf("checkUpdate = %q, %v; want 99.0.0", newest, err)
	}

	client, _ = updateServer(t, "atlas-v"+version, archive, updateSums(t, archive))
	if newest, err := checkUpdate(t.Context(), client, "test/atlas"); err != nil || newest != "" {
		t.Fatalf("checkUpdate(current) = %q, %v; want empty", newest, err)
	}
}

// TestUpdateCommandUpgrades: consent, download, checksum, replace, and the
// restart notice — the whole /update path.
func TestUpdateCommandUpgrades(t *testing.T) {
	archive := updateTar(t, "the new binary")
	client, _ := updateServer(t, "atlas-v99.0.0", archive, updateSums(t, archive))
	exePath := filepath.Join(t.TempDir(), "atlas")
	if err := os.WriteFile(exePath, []byte("the old binary"), 0o755); err != nil {
		t.Fatalf("write the old binary: %v", err)
	}

	var asked string
	consent := func(req tui.PickRequest) tui.PickAnswer {
		asked = req.Title
		return tui.PickAnswer{Value: "upgrade", Taken: true}
	}
	out := &bytes.Buffer{}
	if err := updateCommand(t.Context(), consent, out, "test/atlas", exePath, client); err != nil {
		t.Fatalf("updateCommand: %v", err)
	}
	if asked != "update atlas" {
		t.Fatalf("menu title = %q, want the update menu", asked)
	}
	got, err := os.ReadFile(exePath)
	if err != nil {
		t.Fatalf("read the replaced binary: %v", err)
	}
	if string(got) != "the new binary" {
		t.Fatalf("binary = %q, want the new one", got)
	}
	if !strings.Contains(out.String(), "restart atlas") {
		t.Fatalf("output = %q, want the restart notice", out)
	}
	// The temporary files are gone: a session directory full of half-downloads
	// is a second bug.
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(exePath), ".atlas-*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("leftover files: %v", leftovers)
	}
}

// TestUpdateCommandDeclinedKeepsTheBinary: the menu is the consent, and a
// closed one means no download and no change.
func TestUpdateCommandDeclinedKeepsTheBinary(t *testing.T) {
	archive := updateTar(t, "new")
	client, _ := updateServer(t, "atlas-v99.0.0", archive, updateSums(t, archive))
	exePath := filepath.Join(t.TempDir(), "atlas")
	if err := os.WriteFile(exePath, []byte("old"), 0o755); err != nil {
		t.Fatalf("write the old binary: %v", err)
	}
	declined := func(tui.PickRequest) tui.PickAnswer { return tui.PickAnswer{} }
	out := &bytes.Buffer{}
	if err := updateCommand(t.Context(), declined, out, "test/atlas", exePath, client); err != nil {
		t.Fatalf("updateCommand: %v", err)
	}
	if got, _ := os.ReadFile(exePath); string(got) != "old" {
		t.Fatalf("binary = %q, want it untouched", got)
	}
	if !strings.Contains(out.String(), "staying on") {
		t.Fatalf("output = %q, want the staying notice", out)
	}
}

// TestUpdateCommandSaysNewestWhenCurrent: the command answers the release it
// already runs with a report, not a download.
func TestUpdateCommandSaysNewestWhenCurrent(t *testing.T) {
	archive := updateTar(t, "new")
	client, _ := updateServer(t, "atlas-v"+version, archive, updateSums(t, archive))
	out := &bytes.Buffer{}
	if err := updateCommand(t.Context(), nil, out, "test/atlas", filepath.Join(t.TempDir(), "atlas"), client); err != nil {
		t.Fatalf("updateCommand: %v", err)
	}
	if !strings.Contains(out.String(), "newest release") {
		t.Fatalf("output = %q, want the newest-release report", out)
	}
}

// TestUpdateCommandRefusesABadChecksum: a mismatched archive is the one
// download that must never be installed.
func TestUpdateCommandRefusesABadChecksum(t *testing.T) {
	archive := updateTar(t, "new")
	sums := strings.ReplaceAll(updateSums(t, archive), "0", "1")
	client, _ := updateServer(t, "atlas-v99.0.0", archive, sums)
	exePath := filepath.Join(t.TempDir(), "atlas")
	if err := os.WriteFile(exePath, []byte("old"), 0o755); err != nil {
		t.Fatalf("write the old binary: %v", err)
	}
	err := updateCommand(t.Context(), nil, &bytes.Buffer{}, "test/atlas", exePath, client)
	if err == nil || !strings.Contains(err.Error(), "SHA256SUMS") {
		t.Fatalf("err = %v, want a checksum refusal", err)
	}
	if got, _ := os.ReadFile(exePath); string(got) != "old" {
		t.Fatalf("binary = %q, want it untouched", got)
	}
}

// TestUpdateCommandRefusesAMissingAsset: a release without this platform's
// archive cannot upgrade this machine.
func TestUpdateCommandRefusesAMissingAsset(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/test/atlas/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://github.com/test/atlas/releases/tag/atlas-v99.0.0", http.StatusFound)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := &http.Client{Transport: rewriteTransport{target: strings.TrimPrefix(server.URL, "http://")}}
	err := updateCommand(t.Context(), nil, &bytes.Buffer{}, "test/atlas", filepath.Join(t.TempDir(), "atlas"), client)
	if err == nil || !strings.Contains(err.Error(), updateAssetName()) {
		t.Fatalf("err = %v, want a missing-asset refusal", err)
	}
}

// TestLatestReleaseReadsTheTagFromTheRedirect: the check reads the tag out of
// the 302 Location and never fetches the release page. This is the fix for
// the shared-IP 403: the API rate-limits anonymous traffic per IP, the web
// redirect does not.
func TestLatestReleaseReadsTheTagFromTheRedirect(t *testing.T) {
	var fetched bool
	mux := http.NewServeMux()
	mux.HandleFunc("/test/atlas/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://github.com/test/atlas/releases/tag/atlas-v0.12.25", http.StatusFound)
	})
	mux.HandleFunc("/test/atlas/releases/tag/atlas-v0.12.25", func(w http.ResponseWriter, r *http.Request) {
		fetched = true
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := &http.Client{Transport: rewriteTransport{target: strings.TrimPrefix(server.URL, "http://")}}
	info, err := latestRelease(t.Context(), client, "test/atlas")
	if err != nil || info.TagName != "atlas-v0.12.25" {
		t.Fatalf("latestRelease = %+v, %v; want the tag from the redirect", info, err)
	}
	if fetched {
		t.Fatalf("the release page was fetched; the redirect alone must answer")
	}
}

// TestLatestReleaseRefusesARepoWithoutReleases: a repo with no releases
// redirects /releases/latest to /releases, and that must not read as a tag.
func TestLatestReleaseRefusesARepoWithoutReleases(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/test/atlas/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://github.com/test/atlas/releases", http.StatusFound)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := &http.Client{Transport: rewriteTransport{target: strings.TrimPrefix(server.URL, "http://")}}
	if _, err := latestRelease(t.Context(), client, "test/atlas"); err == nil {
		t.Fatalf("latestRelease succeeded, want a refusal for a repo without releases")
	}
}

// TestUpdateThroughTheCommandTable: /update is wired into the command table,
// and the wiring uses the configured repository and executable.
func TestUpdateThroughTheCommandTable(t *testing.T) {
	oldRepo := updateRepo
	oldClient := updateClient
	oldExec := updateExecutable
	t.Cleanup(func() {
		updateRepo = oldRepo
		updateClient = oldClient
		updateExecutable = oldExec
	})

	cwd := t.TempDir()
	application := commandApp(t, memoryAt(t, cwd), cwd)
	if _, err := runCommand(t, application, "/update now"); err == nil || !strings.Contains(err.Error(), "no argument") {
		t.Fatalf("/update now: err = %v, want an argument refusal", err)
	}

	archive := updateTar(t, "the new binary")
	client, _ := updateServer(t, "atlas-v99.0.0", archive, updateSums(t, archive))
	setUpdateRepo("test/atlas")
	updateClient = func() *http.Client { return client }
	exePath := filepath.Join(t.TempDir(), "atlas")
	if err := os.WriteFile(exePath, []byte("old"), 0o755); err != nil {
		t.Fatalf("write the old binary: %v", err)
	}
	updateExecutable = func() (string, error) { return exePath, nil }

	printed, err := runCommand(t, application, "/update")
	if err != nil {
		t.Fatalf("/update: %v", err)
	}
	if got, _ := os.ReadFile(exePath); string(got) != "the new binary" {
		t.Fatalf("binary = %q, want the new one", got)
	}
	if !strings.Contains(printed, "restart atlas") {
		t.Fatalf("output = %q, want the restart notice", printed)
	}
}

// TestSetUpdateRepo: the default holds until the config names a repository.
func TestSetUpdateRepo(t *testing.T) {
	old := updateRepo
	t.Cleanup(func() { updateRepo = old })

	setUpdateRepo("")
	if updateRepo != defaultUpdateRepo {
		t.Fatalf("empty config: repo = %q, want the default", updateRepo)
	}
	setUpdateRepo("me/atlas")
	if updateRepo != "me/atlas" {
		t.Fatalf("repo = %q, want the override", updateRepo)
	}
}
