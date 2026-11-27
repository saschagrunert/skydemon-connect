// Package exexml adds or removes SkyDemonConnect in MSFS 2024's exe.xml, the
// list of programs the simulator starts with itself.
//
// The file is edited as text rather than re-encoded through encoding/xml,
// so that every other add-on's entry, comment and formatting is kept byte
// for byte.
package exexml

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

const (
	// entryName is the <Name> of our entry.
	entryName = "SkyDemonConnect"
	// autoArg starts the entry's command line: start quietly, exit with
	// MSFS.
	autoArg = "-auto"

	// addonOpen and addonClose are the tags add writes; other add-ons may
	// write them with spaces or attributes, see addonOpenRE.
	addonOpen    = "<Launch.Addon>"
	addonClose   = "</Launch.Addon>"
	commentOpen  = "<!--"
	commentClose = "-->"
	docClose     = "</SimBase.Document>"
	// backupExt is the copy of exe.xml before our first change ever,
	// prevExt the one before our latest change.
	backupExt = ".bak-skydemonconnect"
	prevExt   = ".prev-skydemonconnect"
	fileMode  = 0o644
	maxLatin1 = 0xFF
	// project is where the README shows the entry to add by hand.
	project = "github.com/saschagrunert/skydemon-connect"
	// byHand is what to do about a file this package won't change.
	byHand = "; add the entry by hand, see " + project
	// replaceTries and replaceWait bound the retries of replacing a file
	// that another program, e.g. a virus scanner or MSFS, has open.
	replaceTries = 10
	replaceWait  = 100 * time.Millisecond
)

// skeleton is what a new exe.xml starts as.
const skeleton = `<?xml version="1.0" encoding="Windows-1252"?>
<SimBase.Document Type="SimConnect" version="1,0">
	<Descr>Auto launch external applications on MSFS start</Descr>
	<Filename>exe.xml</Filename>
	<Disabled>False</Disabled>
	<Launch.ManualLoad>False</Launch.ManualLoad>
</SimBase.Document>
`

var (
	// addonOpenRE and addonCloseRE find an entry's tags, also as other
	// tools write them: <Launch.Addon >, <Launch.Addon id="1">.
	addonOpenRE  = regexp.MustCompile(`<Launch\.Addon(?:\s[^>]*)?>`)
	addonCloseRE = regexp.MustCompile(`</Launch\.Addon\s*>`)
	// anyNameRE finds an entry's names, also hand-edited with extra
	// spaces.
	anyNameRE = regexp.MustCompile(`(?s)<Name>\s*(.*?)\s*</Name>`)
	// disabledRE finds an entry switched off in MSFS's own way.
	disabledRE = regexp.MustCompile(`(?i)<Disabled>\s*True\s*</Disabled>`)
	// pathRE finds the entry's <Path>, cmdRE its <CommandLine>.
	pathRE = regexp.MustCompile(`(?s)<Path>(.*?)</Path>`)
	cmdRE  = regexp.MustCompile(`(?s)<CommandLine>(.*?)</CommandLine>`)
	// declRE finds the encoding of the XML declaration, which can only be
	// at the very start of the file. [^>] keeps the match inside it.
	declRE = regexp.MustCompile(`\A\s*<\?xml\b[^>]*?\bencoding\s*=\s*["']([^"']*)["']`)

	errNoInstall = errors.New("MSFS 2024 not found (Microsoft Store or Steam). " +
		"Start MSFS once, or add the entry by hand, see " + project)
	errChanged    = errors.New("changed while it was being edited, try again")
	errNoDocClose = errors.New("exe.xml has no " + docClose + ", not touching it" + byHand)
	errUTF16      = errors.New("exe.xml is UTF-16 or binary, not touching it" + byHand)
	errNotLatin1  = errors.New("character can't be written to a Windows-1252 exe.xml")
	errNotFile    = errors.New("is not a file")
	errBrokenLink = errors.New("is a link to a file that isn't there, not touching it" + byHand)
)

// Installs are the exe.xml files of the MSFS 2024 installations on a PC.
// The files need not exist yet.
type Installs []string

// Find returns the Installs of this PC: Microsoft Store / Xbox app, then
// Steam, as far as their user folders exist.
func Find() Installs { return find(os.Getenv("LOCALAPPDATA"), os.Getenv("APPDATA")) }

// find returns the Installs below the given profile folders.
func find(localAppData, appData string) Installs {
	var out Installs

	for _, dir := range []string{
		filepath.Join(localAppData, "Packages", "Microsoft.Limitless_8wekyb3d8bbwe", "LocalCache"),
		filepath.Join(appData, "Microsoft Flight Simulator 2024"),
	} {
		fi, err := os.Stat(dir)
		if err == nil && fi.IsDir() && filepath.IsAbs(dir) {
			out = append(out, filepath.Join(dir, "exe.xml"))
		}
	}

	return out
}

// State is what the exe.xml files say about SkyDemonConnect.
type State int

const (
	// Absent means no file has an entry for SkyDemonConnect.
	Absent State = iota
	// Other means an entry exists, but not for this exe and these options,
	// e.g. from before the exe was moved, from another copy, or disabled.
	// Set(true) replaces it, Set(false) removes it.
	Other
	// Ours means an entry launches SkyDemonConnect from this exe with
	// these options.
	Ours
)

// State reports whether any of the files launches SkyDemonConnect from
// exePath with args, as Set writes them (Ours), has an entry for it that
// differs (Other), or none (Absent). An entry marked
// <Disabled>True</Disabled> counts as Other.
func (in Installs) State(exePath string, args []string) State {
	state := Absent

	for _, p := range in {
		doc, _, err := read(p)
		if err != nil {
			continue
		}

		switch entryState(doc, exePath, commandLine(args)) {
		case Ours:
			return Ours
		case Other:
			state = Other
		case Absent:
		}
	}

	return state
}

// Set adds (enable) or removes our entry in every file. The entry's
// command line is -auto followed by args, each quoted for the Windows
// command line as needed, e.g. "-name=My Sim". Enabling replaces an
// existing entry, also one that was disabled by hand or has other args.
//
// Before each change of an existing file, it is copied to
// exe.xml.prev-skydemonconnect, and before the first change ever also to
// exe.xml.bak-skydemonconnect, which is kept from then on; if that fails,
// the file is left alone. A file that can't be changed doesn't stop the
// others.
func (in Installs) Set(enable bool, exePath string, args []string) error {
	if len(in) == 0 {
		return errNoInstall
	}

	var errs []error

	commandLine := commandLine(args)

	for _, p := range in {
		err := update(p, enable, exePath, commandLine)
		if err != nil {
			errs = append(errs, inFile(p, err))
		}
	}

	return errors.Join(errs...)
}
