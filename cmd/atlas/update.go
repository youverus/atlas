package main

// Self-update: the release check that runs when the session opens, and the
// /update command that installs a newer release.
//
// The releases live in a public GitHub repository, so both the check and the
// download are anonymous HTTPS — no token, no ssh key, and the same path on a
// server that has neither. A private repository would make the check fail
// silently everywhere its credentials do not exist, which is exactly the
// machine where the reminder matters least.
//
// An update replaces the running binary with the one from the release. That
// is only safe when the archive is what it claims to be, so the download is
// checked against the release's SHA256SUMS before the rename — a public
// repository is where the check costs the least and matters the most.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"pi-go/internal/tui"
)

// defaultUpdateRepo is the public repository the release check and /update
// read. It is the one this binary is published to, and a config override is
// for a reader who publishes their own fork of the releases.
const defaultUpdateRepo = "youverus/atlas"

// updateTimeout bounds one release check. The check runs while the session
// opens, and a network that never answers must not stall the first frame
// longer than a reader will tolerate.
const updateTimeout = 10 * time.Second

// updateRepo is the repository in effect. It is set once from config before
// the interface starts, so the command table and the startup check read the
// same source without threading a string through every call.
var updateRepo = defaultUpdateRepo

// setUpdateRepo installs the repository the config names. An empty config
// value keeps the default: the release repository is not a secret, and a
// reader who never set it should still get the check.
func setUpdateRepo(repo string) {
	if repo != "" {
		updateRepo = repo
	}
}

// updateClient is the HTTP client the check and the download share. A var so
// a test can swap in a client that answers from its own server.
var updateClient = func() *http.Client {
	return &http.Client{Timeout: updateTimeout}
}

// updateExecutable is where the update lands. It is a function rather than a
// direct call so a test can point it at a file it owns.
var updateExecutable = os.Executable

// releaseInfo is the part of a GitHub release the update reads.
type releaseInfo struct {
	TagName string      `json:"tag_name"`
	Assets  []assetInfo `json:"assets"`
}

// assetInfo is one file attached to the release.
type assetInfo struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// latestRelease reads the newest release of the repository.
func latestRelease(ctx context.Context, client *http.Client, repo string) (releaseInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo), nil)
	if err != nil {
		return releaseInfo{}, fmt.Errorf("update: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return releaseInfo{}, fmt.Errorf("update: check %s: %w", repo, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return releaseInfo{}, fmt.Errorf("update: check %s: GitHub says %s", repo, resp.Status)
	}
	var info releaseInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&info); err != nil {
		return releaseInfo{}, fmt.Errorf("update: decode the release: %w", err)
	}
	return info, nil
}

// parseAtlasVersion reads the version out of a release tag. The tag space of
// the repository is shared with nothing, but the prefix still matters: a tag
// that does not say atlas must not be read as the newest atlas release.
func parseAtlasVersion(tag string) (string, error) {
	rest, ok := strings.CutPrefix(tag, "atlas-v")
	if !ok || rest == "" {
		return "", fmt.Errorf("update: tag %q is not an atlas release", tag)
	}
	for _, part := range strings.Split(rest, ".") {
		if part == "" {
			return "", fmt.Errorf("update: tag %q is not a version", tag)
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return "", fmt.Errorf("update: tag %q is not a version", tag)
			}
		}
	}
	return rest, nil
}

// compareVersions orders two dotted versions. A part that is not a number
// falls back to string order, so a malformed version can never compare equal
// to a real one.
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aerr := strconv.Atoi(as[i])
		bn, berr := strconv.Atoi(bs[i])
		if aerr != nil || berr != nil {
			return strings.Compare(a, b)
		}
		if an < bn {
			return -1
		}
		if an > bn {
			return 1
		}
	}
	return strings.Compare(a, b)
}

// checkUpdate asks the release repository for the newest atlas. An empty
// result with no error means this build is the newest one.
func checkUpdate(ctx context.Context, client *http.Client, repo string) (string, error) {
	info, err := latestRelease(ctx, client, repo)
	if err != nil {
		return "", err
	}
	newest, err := parseAtlasVersion(info.TagName)
	if err != nil {
		return "", err
	}
	if compareVersions(newest, version) <= 0 {
		return "", nil
	}
	return newest, nil
}

// updateCommand is /update: report what is current, and with the reader's
// consent replace this binary with the newest release. The pick menu is the
// consent; a caller with no menu already said yes by typing the command.
func updateCommand(ctx context.Context, pick chooser, out io.Writer, repo, exePath string, client *http.Client) error {
	if runtime.GOOS == "windows" {
		return errors.New("/update: self-update is not supported on windows")
	}
	if exePath == "" {
		return errors.New("/update: cannot find this binary")
	}
	info, err := latestRelease(ctx, client, repo)
	if err != nil {
		return err
	}
	newest, err := parseAtlasVersion(info.TagName)
	if err != nil {
		return err
	}
	if compareVersions(newest, version) <= 0 {
		fmt.Fprintf(out, "atlas %s is the newest release\n", version)
		return nil
	}
	if pick != nil {
		answer := pick(tui.PickRequest{
			Title: "update atlas",
			Items: []tui.Choice{
				{Value: "upgrade", Label: "upgrade to " + newest},
				{Value: "keep", Label: "stay on " + version},
			},
		})
		if !answer.Taken || answer.Value != "upgrade" {
			fmt.Fprintf(out, "staying on atlas %s; /update runs the upgrade\n", version)
			return nil
		}
	}
	fmt.Fprintf(out, "updating atlas %s -> %s\n", version, newest)
	if err := applyUpdate(ctx, client, info, exePath); err != nil {
		return err
	}
	fmt.Fprintf(out, "updated to atlas %s; restart atlas to run it\n", newest)
	return nil
}

// applyUpdate downloads the archive for this platform, checks it against the
// release's SHA256SUMS, and renames it over the running binary. The rename is
// atomic, so a crash in the middle leaves either the old binary or the new
// one, never a half-written file.
func applyUpdate(ctx context.Context, client *http.Client, info releaseInfo, exePath string) error {
	asset := "atlas-" + runtime.GOOS + "-" + runtime.GOARCH + ".tar.gz"
	var assetURL, sumsURL string
	for _, a := range info.Assets {
		switch a.Name {
		case asset:
			assetURL = a.URL
		case "SHA256SUMS":
			sumsURL = a.URL
		}
	}
	if assetURL == "" {
		return fmt.Errorf("/update: the release has no %s asset", asset)
	}
	if sumsURL == "" {
		return errors.New("/update: the release has no SHA256SUMS; refusing to install an unchecked archive")
	}

	dir := filepath.Dir(exePath)
	archive, err := os.CreateTemp(dir, ".atlas-update-*.tar.gz")
	if err != nil {
		return fmt.Errorf("/update: %w", err)
	}
	defer os.Remove(archive.Name())
	if err := downloadTo(ctx, client, assetURL, archive); err != nil {
		archive.Close()
		return fmt.Errorf("/update: download %s: %w", asset, err)
	}
	if err := archive.Close(); err != nil {
		return fmt.Errorf("/update: %w", err)
	}

	sums, err := downloadText(ctx, client, sumsURL)
	if err != nil {
		return fmt.Errorf("/update: download SHA256SUMS: %w", err)
	}
	expected := checksumFor(sums, asset)
	if expected == "" {
		return fmt.Errorf("/update: SHA256SUMS names no %s", asset)
	}
	actual, err := sha256File(archive.Name())
	if err != nil {
		return fmt.Errorf("/update: %w", err)
	}
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("/update: the downloaded %s does not match SHA256SUMS; refusing to install it", asset)
	}

	next := filepath.Join(dir, fmt.Sprintf(".atlas-next-%d", os.Getpid()))
	if err := extractBinary(archive.Name(), strings.TrimSuffix(asset, ".tar.gz"), next); err != nil {
		os.Remove(next)
		return err
	}
	defer os.Remove(next)
	if err := os.Rename(next, exePath); err != nil {
		return fmt.Errorf("/update: replace this binary: %w", err)
	}
	return nil
}

// downloadTo streams one URL into a writer.
func downloadTo(ctx context.Context, client *http.Client, url string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub says %s", resp.Status)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

// downloadText reads one URL into a string, bounded so a hostile or broken
// server cannot balloon memory.
func downloadText(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub says %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// checksumFor finds the sha256 line for one asset in a SHA256SUMS file.
func checksumFor(sums, asset string) string {
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == asset {
			return fields[0]
		}
	}
	return ""
}

// sha256File hashes one file.
func sha256File(path string) (string, error) {
	handle, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer handle.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, handle); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// extractBinary writes the named entry of a tar.gz into dest, executable.
func extractBinary(archive, entry, dest string) error {
	handle, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer handle.Close()
	gz, err := gzip.NewReader(handle)
	if err != nil {
		return fmt.Errorf("/update: %s is not gzip: %w", archive, err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("/update: %s is not in the archive", entry)
		}
		if err != nil {
			return fmt.Errorf("/update: read the archive: %w", err)
		}
		if header.Name != entry {
			continue
		}
		out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
		if err != nil {
			return fmt.Errorf("/update: %w", err)
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return fmt.Errorf("/update: extract %s: %w", entry, err)
		}
		if err := out.Close(); err != nil {
			return fmt.Errorf("/update: %w", err)
		}
		return nil
	}
}
