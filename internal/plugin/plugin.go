// Package plugin installs SkyDemonConnect's plugin, an MSFS package, into
// the Community folder of each MSFS 2024 installation, and keeps it at the
// version built into the exe.
//
// The package, package/html_ui, holds plain JavaScript for avionics, one
// folder each: G1000 for the G1000's MFD, which asks the route server for
// the route last saved in SkyDemon and loads it into the flight plan.
package plugin

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/saschagrunert/skydemon-connect/internal/exexml"
)

// Name is the package's folder in the Community folder.
const Name = "skydemon-connect-plugin"

// creator marks the package as SkyDemonConnect's: one of another creator
// under the same name is left alone.
const creator = "SkyDemonConnect"

//go:embed all:package
var embedded embed.FS

const (
	root     = "package"
	dirMode  = 0o755
	fileMode = 0o644
	// minGameVersion is MSFS 2024's first release.
	minGameVersion = "1.0.0"
	userCfg        = "UserCfg.opt"
	// idPlaceholder in the package's files becomes ID when installed, so
	// that the plugin can tell SkyDemonConnect which one MSFS loaded.
	idPlaceholder = "@SKYDEMON_CONNECT_PLUGIN_ID@"
	idLen         = 16
)

var (
	errNoPackagesPath = errors.New("no InstalledPackagesPath in " + userCfg)
	errNotOurs        = errors.New("is another package of the same name, not touching it")
	errLinked         = errors.New("is a link, e.g. to a copy in development, not touching it")

	// packagesPathRE finds InstalledPackagesPath "D:\MSFS 2024\Packages".
	packagesPathRE = regexp.MustCompile(`^\s*InstalledPackagesPath\s+"([^"]+)"`)
	// versionRE finds the numbers of a version such as v0.2.0 or
	// v0.2.0-3-gabcdef.
	versionRE = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)`)
)

// Result is what Install did for one installation.
type Result struct {
	// Community is the Community folder, "" when it wasn't found.
	Community string
	Err       error
	// Changed is whether the package was installed or replaced, or, with
	// Err, failed to be.
	Changed bool
	// Outdated is whether an older package was left in place, as Install
	// was told not to replace it.
	Outdated bool
}

// Install brings the package up to date in the Community folder named by
// the UserCfg.opt in each of userDirs, MSFS's user folders (where exe.xml
// is). version is SkyDemonConnect's, for the package's manifest.
//
// A missing package is always installed. An older one is only replaced
// with replace: MSFS indexes the packages when it starts, and files changed
// while it runs may load half old, half new. Without replace it is left
// alone and reported as Outdated.
func Install(userDirs []string, version string, replace bool) []Result {
	out := make([]Result, 0, len(userDirs))

	for _, dir := range userDirs {
		community, err := communityFolder(dir)
		if err != nil {
			out = append(out, Result{Community: "", Err: err, Changed: false, Outdated: false})

			continue
		}

		r := installInto(community, version, replace)
		r.Community = community
		out = append(out, r)
	}

	return out
}

// ID identifies the package's files: the plugin sends the ID of the one MSFS
// loaded.
func ID() string {
	_, id, err := files()
	if err != nil {
		return ""
	}

	return id
}

// communityFolder is the Community folder below the packages folder that
// the UserCfg.opt in dir names.
func communityFolder(dir string) (string, error) {
	f, err := os.Open(filepath.Join(dir, userCfg))
	if err != nil {
		return "", fmt.Errorf("MSFS's settings: %w", err)
	}

	defer func() { _ = f.Close() }()

	lines := bufio.NewScanner(f)
	for lines.Scan() {
		match := packagesPathRE.FindStringSubmatch(lines.Text())
		if match == nil {
			continue
		}

		// A folder below a user name such as Jürgen may be written in the
		// ANSI code page rather than UTF-8.
		packages := match[1]
		if !utf8.ValidString(packages) {
			packages = exexml.Decode1252([]byte(packages))
		}

		community := filepath.Join(packages, "Community")
		if !filepath.IsAbs(community) {
			break
		}

		return community, nil
	}

	err = lines.Err()
	if err != nil {
		return "", fmt.Errorf("MSFS's settings: %w", err)
	}

	return "", fmt.Errorf("%w in %s", errNoPackagesPath, dir)
}

// files are the package's files, by their path in the package, with the
// placeholder replaced by their ID, a hash of the files as embedded.
func files() (map[string][]byte, string, error) {
	out := map[string][]byte{}

	err := fs.WalkDir(embedded, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		b, err := embedded.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}

		out[strings.TrimPrefix(p, root+"/")] = b

		return nil
	})
	if err != nil {
		return nil, "", fmt.Errorf("plugin files: %w", err)
	}

	names := slices.Sorted(maps.Keys(out))

	h := sha256.New()
	for _, name := range names {
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00", name, len(out[name])) // a hash doesn't fail
		_, _ = h.Write(out[name])
	}

	id := hex.EncodeToString(h.Sum(nil))[:idLen]
	for name, b := range out {
		out[name] = bytes.ReplaceAll(b, []byte(idPlaceholder), []byte(id))
	}

	return out, id, nil
}

// installInto installs the package into community, unless the same files
// are there, or, without update, any package of ours. A changed package is
// written next to the old one and then swapped in, so that MSFS never sees
// half of it.
func installInto(community, version string, update bool) Result {
	var r Result

	info, err := os.Stat(community)
	if err != nil {
		r.Err = fmt.Errorf("community folder: %w", err)

		return r
	}

	if !info.IsDir() {
		r.Err = fmt.Errorf("community folder %s: %w", community, errNotDir)

		return r
	}

	content, _, err := files()
	if err != nil {
		r.Err = err

		return r
	}

	removeLeftovers(community)

	target := filepath.Join(community, Name)

	state, err := installed(target, content)

	switch {
	case err != nil:
		r.Err = err
	case state == current:
	case state == older && !update:
		r.Outdated = true
	default:
		r.Changed, r.Err = true, replace(community, target, content, version)
	}

	return r
}

var errNotDir = errors.New("is not a folder")

// removeLeftovers removes what an install that failed halfway left in
// community, e.g. an old package whose files MSFS held open.
func removeLeftovers(community string) {
	leftovers, _ := filepath.Glob(filepath.Join(community, "."+Name+"-new-*"))
	for _, p := range leftovers {
		_ = os.RemoveAll(p)
	}
}

// packageState is what a Community folder holds of the package.
type packageState int

const (
	missing packageState = iota
	older
	current
)

// installed reports whether target holds content, older files or nothing.
// A target that is not SkyDemonConnect's package is an error.
func installed(target string, content map[string][]byte) (packageState, error) {
	info, err := os.Lstat(target)
	if errors.Is(err, fs.ErrNotExist) {
		return missing, nil
	}

	if err != nil {
		return missing, fmt.Errorf("%s: %w", target, err)
	}

	// A junction, as tools that link packages into the Community folder
	// make, is irregular on Windows rather than a symlink.
	if info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
		return missing, fmt.Errorf("%s %w", target, errLinked)
	}

	man, err := readManifest(target)
	if err != nil || man.Creator != creator {
		return missing, fmt.Errorf("%s %w", target, errNotOurs)
	}

	// Without its layout.json MSFS loads none of the package's files.
	_, err = os.Stat(filepath.Join(target, "layout.json"))
	if err != nil {
		return older, nil //nolint:nilerr // broken: install again
	}

	for name, want := range content {
		got, err := os.ReadFile(filepath.Join(target, filepath.FromSlash(name)))
		if err != nil || !bytes.Equal(got, want) {
			return older, nil //nolint:nilerr // a file missing or changed: install again
		}
	}

	return current, nil
}

func replace(community, target string, content map[string][]byte, version string) error {
	tmp, err := os.MkdirTemp(community, "."+Name+"-new-")
	if err != nil {
		return fmt.Errorf("install: %w", err)
	}

	defer func() { _ = os.RemoveAll(tmp) }()

	err = write(tmp, content, version)
	if err != nil {
		return err
	}

	// The old package goes aside first: a folder can't be renamed over
	// another.
	old := ""

	_, err = os.Lstat(target)
	if err == nil {
		old = tmp + "-old"

		err = os.Rename(target, old)
		if err != nil {
			return fmt.Errorf("replace %s (is MSFS running?): %w", target, err)
		}
	}

	err = os.Rename(tmp, target)
	if err != nil {
		if old != "" {
			_ = os.Rename(old, target)
		}

		return fmt.Errorf("install %s: %w", target, err)
	}

	if old != "" {
		_ = os.RemoveAll(old)
	}

	return nil
}

// manifest is MSFS's manifest.json of a package, in MSFS's names.
//
//nolint:tagliatelle // MSFS's own format
type manifest struct {
	Dependencies       []string `json:"dependencies"`
	ContentType        string   `json:"content_type"`
	Title              string   `json:"title"`
	Manufacturer       string   `json:"manufacturer"`
	Creator            string   `json:"creator"`
	PackageVersion     string   `json:"package_version"`
	MinimumGameVersion string   `json:"minimum_game_version"`
	TotalPackageSize   string   `json:"total_package_size"`
}

func readManifest(dir string) (manifest, error) {
	var man manifest

	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return man, fmt.Errorf("manifest: %w", err)
	}

	err = json.Unmarshal(b, &man)
	if err != nil {
		return man, fmt.Errorf("manifest: %w", err)
	}

	return man, nil
}

// layoutEntry is one file in MSFS's layout.json, which lists every file of
// a package but itself and the manifest.
type layoutEntry struct {
	Path string `json:"path"`
	Size int    `json:"size"`
	// Date is a Windows FILETIME: 100 ns since 1601.
	Date int64 `json:"date"`
}

// fileTimeEpoch is 1601-01-01 in Unix seconds.
const (
	fileTimeEpoch  = -11644473600
	nsPerTick      = 100
	ticksPerSecond = 1e7
)

func fileTime(t time.Time) int64 {
	return (t.Unix()-fileTimeEpoch)*ticksPerSecond + int64(t.Nanosecond())/nsPerTick
}

// write writes the package with content into dir.
func write(dir string, content map[string][]byte, version string) error {
	now := fileTime(time.Now())
	layout := struct {
		Content []layoutEntry `json:"content"`
	}{Content: make([]layoutEntry, 0, len(content))}
	total := 0

	for name, b := range content {
		p := filepath.Join(dir, filepath.FromSlash(name))

		err := os.MkdirAll(filepath.Dir(p), dirMode)
		if err != nil {
			return fmt.Errorf("install: %w", err)
		}

		err = os.WriteFile(p, b, fileMode)
		if err != nil {
			return fmt.Errorf("install: %w", err)
		}

		layout.Content = append(layout.Content,
			layoutEntry{Path: path.Clean(name), Size: len(b), Date: now})
		total += len(b)
	}

	slices.SortFunc(layout.Content, func(a, b layoutEntry) int {
		return strings.Compare(a.Path, b.Path)
	})

	man := manifest{
		Dependencies:       []string{},
		ContentType:        "MISC",
		Title:              "SkyDemonConnect: SkyDemon routes in the avionics",
		Manufacturer:       "",
		Creator:            creator,
		PackageVersion:     packageVersion(version),
		MinimumGameVersion: minGameVersion,
		TotalPackageSize:   fmt.Sprintf("%020d", total),
	}

	for name, v := range map[string]any{"layout.json": layout, "manifest.json": man} {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return fmt.Errorf("install: %w", err)
		}

		err = os.WriteFile(filepath.Join(dir, name), append(b, '\n'), fileMode)
		if err != nil {
			return fmt.Errorf("install: %w", err)
		}
	}

	return nil
}

// packageVersion is version as MSFS wants it, three numbers: v0.2.0 is
// 0.2.0, a development build 0.0.0.
func packageVersion(version string) string {
	m := versionRE.FindStringSubmatch(version)
	if m == nil {
		return "0.0.0"
	}

	return m[1] + "." + m[2] + "." + m[3]
}
