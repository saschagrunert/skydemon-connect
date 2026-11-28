package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// msfs makes an MSFS user folder whose UserCfg.opt names a packages folder
// with a Community folder, as MSFS writes it, and returns both.
func msfs(t *testing.T) (string, string) {
	t.Helper()

	user, packages := t.TempDir(), t.TempDir()
	community := filepath.Join(packages, "Community")

	err := os.Mkdir(community, 0o700)
	if err != nil {
		t.Fatal(err)
	}

	cfg := "Version 75\r\n{Graphics\r\n\tVersion 1.1\r\n}\r\nInstalledPackagesPath \"" + packages + "\"\r\n"

	err = os.WriteFile(filepath.Join(user, userCfg), []byte(cfg), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	return user, community
}

func installOnce(t *testing.T, user, version string) Result {
	t.Helper()

	return installWith(t, user, version, true)
}

func installWith(t *testing.T, user, version string, replace bool) Result {
	t.Helper()

	r := Install([]string{user}, version, replace)
	if len(r) != 1 {
		t.Fatalf("results %+v", r)
	}

	return r[0]
}

func TestInstall(t *testing.T) {
	t.Parallel()

	user, community := msfs(t)

	r := installOnce(t, user, "v0.2.0-3-gabcdef")
	if r.Err != nil || !r.Changed || r.Community != community {
		t.Fatalf("first install %+v", r)
	}

	target := filepath.Join(community, Name)

	content, id, err := files()
	if err != nil {
		t.Fatal(err)
	}

	if id == "" || id != ID() {
		t.Errorf("ID %q, files say %q", ID(), id)
	}

	js := string(content["html_ui/SkyDemonConnect/G1000/route-plugin.js"])
	if strings.Contains(js, idPlaceholder) || !strings.Contains(js, `"`+id+`"`) {
		t.Error("plugin without its ID")
	}

	for name, want := range content {
		got, err := os.ReadFile(filepath.Join(target, filepath.FromSlash(name)))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s: %v", name, err)
		}
	}

	man, err := readManifest(target)
	if err != nil || man.Creator != creator || man.PackageVersion != "0.2.0" ||
		man.ContentType != "MISC" {
		t.Errorf("manifest %+v, %v", man, err)
	}

	var layout struct {
		Content []layoutEntry `json:"content"`
	}

	b, err := os.ReadFile(filepath.Join(target, "layout.json"))
	if err == nil {
		err = json.Unmarshal(b, &layout)
	}

	if err != nil || len(layout.Content) != len(content) {
		t.Fatalf("layout %s, %v", b, err)
	}

	total := 0

	for i, e := range layout.Content {
		if len(content[e.Path]) != e.Size || (i > 0 && layout.Content[i-1].Path >= e.Path) {
			t.Errorf("layout entry %+v", e)
		}

		total += e.Size
	}

	if man.TotalPackageSize != fmt.Sprintf("%020d", total) {
		t.Errorf("total size %s, want %d", man.TotalPackageSize, total)
	}
}

// An installed package is left alone while up to date, and replaced when
// its files changed, unless told not to.
func TestInstallUpdate(t *testing.T) {
	t.Parallel()

	user, community := msfs(t)
	target := filepath.Join(community, Name)

	if r := installOnce(t, user, "v0.2.0"); r.Err != nil || !r.Changed {
		t.Fatalf("first install %+v", r)
	}

	// Up to date: nothing to do, also for a new SkyDemonConnect version.
	if r := installOnce(t, user, "v0.3.0"); r.Err != nil || r.Changed {
		t.Errorf("second install %+v", r)
	}

	// A changed file is replaced.
	script := filepath.Join(target, "html_ui", "SkyDemonConnect", "G1000", "route-plugin.js")

	err := os.WriteFile(script, []byte("old"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	// With MSFS running it is left for later, but reported.
	if r := installWith(t, user, "v0.3.0", false); r.Err != nil || r.Changed || !r.Outdated {
		t.Errorf("not replacing %+v", r)
	}

	if b, _ := os.ReadFile(script); string(b) != "old" {
		t.Error("replaced while told not to")
	}

	if r := installOnce(t, user, "v0.3.0"); r.Err != nil || !r.Changed || r.Outdated {
		t.Errorf("after a change %+v", r)
	}

	if b, _ := os.ReadFile(script); string(b) == "old" {
		t.Error("changed file kept")
	}

	if m, _ := readManifest(target); m.PackageVersion != "0.3.0" {
		t.Errorf("version %q", m.PackageVersion)
	}

	if left, _ := filepath.Glob(filepath.Join(community, ".*")); len(left) > 0 {
		t.Errorf("left behind %v", left)
	}

	// Without its layout.json MSFS loads nothing of it: installed again.
	err = os.Remove(filepath.Join(target, "layout.json"))
	if err != nil {
		t.Fatal(err)
	}

	if r := installOnce(t, user, "v0.3.0"); r.Err != nil || !r.Changed {
		t.Errorf("without layout.json %+v", r)
	}
}

// A missing package is installed also while MSFS runs: MSFS doesn't know
// it yet.
func TestInstallNewWhileRunning(t *testing.T) {
	t.Parallel()

	user, community := msfs(t)

	if r := installWith(t, user, "v1.0.0", false); r.Err != nil || !r.Changed || r.Outdated {
		t.Errorf("install %+v", r)
	}

	_, err := os.Stat(filepath.Join(community, Name, "layout.json"))
	if err != nil {
		t.Error(err)
	}
}

// A packages folder below a user name with an umlaut, written in the ANSI
// code page.
func TestCommunityFolderANSI(t *testing.T) {
	t.Parallel()

	user, base := t.TempDir(), t.TempDir()
	community := filepath.Join(base, "Jürgen", "Packages", "Community")

	err := os.MkdirAll(community, 0o700)
	if err != nil {
		t.Fatal(err)
	}

	ansi := strings.ReplaceAll(filepath.Join(base, "Jürgen", "Packages"), "ü", "\xfc")

	err = os.WriteFile(filepath.Join(user, userCfg),
		[]byte("InstalledPackagesPath \""+ansi+"\"\r\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	got, err := communityFolder(user)
	if err != nil || got != community {
		t.Errorf("got %q, %v; want %q", got, err, community)
	}
}

func TestInstallLeavesOthersAlone(t *testing.T) {
	t.Parallel()

	user, community := msfs(t)
	target := filepath.Join(community, Name)

	err := os.MkdirAll(target, 0o700)
	if err != nil {
		t.Fatal(err)
	}

	err = os.WriteFile(filepath.Join(target, "manifest.json"),
		[]byte(`{"creator":"Someone"}`), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	if r := installOnce(t, user, "v1.0.0"); !errors.Is(r.Err, errNotOurs) || r.Changed ||
		!r.Foreign {
		t.Errorf("other creator %+v", r)
	}

	// Without a manifest it isn't known to be ours either.
	err = os.Remove(filepath.Join(target, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}

	if r := installOnce(t, user, "v1.0.0"); !errors.Is(r.Err, errNotOurs) {
		t.Errorf("no manifest %+v", r)
	}

	linkUser, linkCommunity := msfs(t)

	err = os.Symlink(t.TempDir(), filepath.Join(linkCommunity, Name))
	if err != nil {
		t.Skipf("no symlinks here: %v", err)
	}

	if r := installOnce(t, linkUser, "v1.0.0"); !errors.Is(r.Err, errLinked) || !r.Foreign {
		t.Errorf("linked %+v", r)
	}
}

// An install that failed halfway, e.g. with MSFS holding the old files,
// leaves folders that the next one removes.
func TestInstallRemovesLeftovers(t *testing.T) {
	t.Parallel()

	user, community := msfs(t)
	leftover := filepath.Join(community, "."+Name+"-new-123-old")
	// Another copy's, at work: too young to be a leftover.
	young := filepath.Join(community, "."+Name+"-new-456")

	for _, dir := range []string{leftover, young} {
		err := os.MkdirAll(filepath.Join(dir, "html_ui"), 0o700)
		if err != nil {
			t.Fatal(err)
		}
	}

	old := time.Now().Add(-leftoverAge - time.Minute)

	err := os.Chtimes(leftover, old, old)
	if err != nil {
		t.Fatal(err)
	}

	if r := installOnce(t, user, "dev"); r.Err != nil || !r.Changed {
		t.Fatalf("install %+v", r)
	}

	_, err = os.Stat(leftover)
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("leftover still there: %v", err)
	}

	_, err = os.Stat(young)
	if err != nil {
		t.Errorf("another copy's folder removed: %v", err)
	}

	if m, _ := readManifest(filepath.Join(community, Name)); m.PackageVersion != "0.0.0" {
		t.Errorf("dev version %q", m.PackageVersion)
	}
}

func TestInstallWithoutMSFS(t *testing.T) {
	t.Parallel()

	noCfg := t.TempDir()

	noPath := t.TempDir()

	err := os.WriteFile(filepath.Join(noPath, userCfg), []byte("Version 75\r\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	relative := t.TempDir()

	err = os.WriteFile(filepath.Join(relative, userCfg),
		[]byte(`InstalledPackagesPath "Packages"`+"\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	noCommunity := t.TempDir()

	err = os.WriteFile(filepath.Join(noCommunity, userCfg),
		[]byte(`InstalledPackagesPath "`+filepath.Join(t.TempDir(), "gone")+`"`+"\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	results := Install([]string{noCfg, noPath, relative, noCommunity}, "v1.0.0", true)
	if len(results) != 4 {
		t.Fatalf("results %+v", results)
	}

	for i, r := range results {
		if r.Err == nil || r.Changed {
			t.Errorf("%d: %+v", i, r)
		}
	}

	if !errors.Is(results[1].Err, errNoPackagesPath) ||
		!errors.Is(results[2].Err, errNoPackagesPath) {
		t.Errorf("no path: %v; relative: %v", results[1].Err, results[2].Err)
	}

	if !errors.Is(results[3].Err, os.ErrNotExist) || results[3].Community == "" {
		t.Errorf("no community folder: %+v", results[3])
	}
}

func TestFileTime(t *testing.T) {
	t.Parallel()

	// 2026-11-28 12:00:00 UTC as Windows shows it.
	if got := fileTime(time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)); got != 134403408000000000 {
		t.Errorf("got %d", got)
	}
}

func TestPackageVersion(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"v0.1.0": "0.1.0", "v1.2.3-4-gdeadbee-dirty": "1.2.3", "2.0.0": "2.0.0", "dev": "0.0.0", "": "0.0.0",
	} {
		if got := packageVersion(in); got != want {
			t.Errorf("packageVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
