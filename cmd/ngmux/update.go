package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"
)

// updateRepo is the GitHub "owner/name" the update command pulls releases from.
const updateRepo = "NexuraGrid/ng_mux"

// versionString is the build version, falling back to the module version the Go
// toolchain records in `go install`-ed binaries so those still report something
// useful even without the release ldflags.
func versionString() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return "dev"
}

func hasFlag(args []string, f string) bool {
	for _, a := range args {
		if a == f {
			return true
		}
	}
	return false
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// selfUpdate downloads the latest release binary for this OS/arch from GitHub
// and replaces the running executable in place. force re-installs even when the
// running version already matches the latest tag.
func selfUpdate(out io.Writer, force bool) error {
	cur := versionString()
	fmt.Fprintf(out, "current version: %s\n", cur)

	rel, err := latestRelease()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "latest release:  %s\n", rel.TagName)
	if !force && rel.TagName == cur {
		fmt.Fprintln(out, "already up to date")
		return nil
	}

	assetName := fmt.Sprintf("ngmux_%s_%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		assetName += ".exe"
	}
	var dlURL string
	for _, a := range rel.Assets {
		if a.Name == assetName {
			dlURL = a.URL
			break
		}
	}
	if dlURL == "" {
		return fmt.Errorf("release %s has no binary for %s/%s (expected asset %q)",
			rel.TagName, runtime.GOOS, runtime.GOARCH, assetName)
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	fmt.Fprintf(out, "downloading %s ...\n", assetName)
	blob, err := download(dlURL)
	if err != nil {
		return err
	}
	if sum, err := expectedSum(rel, assetName); err == nil && sum != "" {
		got := sha256.Sum256(blob)
		if hex.EncodeToString(got[:]) != sum {
			return fmt.Errorf("checksum mismatch for %s: the download may be corrupt", assetName)
		}
	}

	if err := replaceExecutable(exe, blob); err != nil {
		return err
	}
	fmt.Fprintf(out, "updated to %s — restart any running ngmux clients\n", rel.TagName)
	return nil
}

func httpClient() *http.Client { return &http.Client{Timeout: 60 * time.Second} }

func latestRelease() (*ghRelease, error) {
	req, _ := http.NewRequest(http.MethodGet,
		"https://api.github.com/repos/"+updateRepo+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("no releases published for %s yet", updateRepo)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %s", resp.Status)
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

func download(url string) ([]byte, error) {
	resp, err := httpClient().Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// expectedSum fetches the release's SHA256SUMS asset, if any, and returns the
// hex digest listed for assetName ("" when there is no such file or entry).
func expectedSum(rel *ghRelease, assetName string) (string, error) {
	var url string
	for _, a := range rel.Assets {
		if a.Name == "SHA256SUMS" {
			url = a.URL
			break
		}
	}
	if url == "" {
		return "", nil
	}
	body, err := download(url)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == assetName {
			return f[0], nil
		}
	}
	return "", nil
}

// replaceExecutable swaps the file at exe for one holding blob. It writes a
// sibling temp file first so a failed write never leaves a half-written binary,
// then renames it over exe — moving the old image aside first on Windows, where
// a running executable cannot be replaced directly.
func replaceExecutable(exe string, blob []byte) error {
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".ngmux-update-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s (try re-running the install script, or with sudo): %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(blob); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return err
	}

	if runtime.GOOS == "windows" {
		old := exe + ".old"
		os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			return err
		}
		if err := os.Rename(tmpName, exe); err != nil {
			os.Rename(old, exe) // roll back
			return err
		}
		os.Remove(old) // still mapped while we run; cleaned up on the next update
		return nil
	}
	if err := os.Rename(tmpName, exe); err != nil {
		return fmt.Errorf("cannot replace %s (try re-running the install script, or with sudo): %w", exe, err)
	}
	return nil
}
