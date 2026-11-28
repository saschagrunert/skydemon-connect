# SkyDemonConnect in detail

What the [README](../README.md) leaves out: the tray menu, starting with
MSFS, the demo, traffic, SkyDemon routes in the G1000, updating, all options
and how to build it.

## Tray icon

| Icon  | Meaning                                                     |
| ----- | ----------------------------------------------------------- |
| green | sending positions; hover for altitude, speed and track      |
| grey  | waiting for MSFS: not running, loading or in its menu       |
| red   | a problem, e.g. `SimConnect.dll` missing; hover for details |

The menu shows the status and where positions go (UDP addresses, the COM
port), then **Start with MSFS**, **Open log** and **Quit**. An item is greyed
out when it can't work: **Open log** when no folder for the log was
writable, **Start with MSFS** when no MSFS 2024 install was found or the
exe's own path is unknown. The item says which.

In MSFS's menus no position is sent and SkyDemon shows the GPS as lost.
During an active pause (the Pause key) the last position keeps being sent,
standing still once MSFS's frames stop changing. Whether MSFS 2024 counts its
Esc menu as a menu or as a pause is not verified yet; the log says which
(`simconnect: no flight running` or nothing).

Only one copy runs at a time. Each start writes a fresh `SkyDemonConnect.log`
next to the exe (or in `%LOCALAPPDATA%\SkyDemonConnect` if that folder is
read-only) and keeps the previous one as `SkyDemonConnect.previous.log`;
`-console` writes none. A copy run from the same folder in another Windows
session, or started by MSFS while another copy runs, logs to
`SkyDemonConnect.<number>.log` instead, which is deleted after a week.

## Start with MSFS

**Start with MSFS** adds the exe to MSFS's `exe.xml` (Microsoft Store and
Steam), so that MSFS starts it and it quits with MSFS. The entry gets the
options this copy was started with that differ from the defaults, e.g.
`-udp`, but not `-auto`, `-console`, `-demo`, `-demo-lat`, `-demo-lon`,
`-test-traffic`, `-quiet` or `-version`, which only make sense for one
start. `-no-route` and `-routes` are passed on.

The original `exe.xml` is backed up once to `exe.xml.bak-skydemonconnect`
(an empty one if there was none), and the file as it was before the latest
change to `exe.xml.prev-skydemonconnect`. After moving the exe, or to change
the options, start it the new way: the menu item then reads "Start with MSFS
(not set up for this copy, click to fix)", and a click replaces the entry
with this copy's.

MSFS starts it with `-auto`, and it exits when MSFS exits. If it can't tell
that MSFS started it, it exits when MSFS's SimConnect session ends instead,
and keeps running if MSFS closes before a session started.

To add it by hand, edit
`%LOCALAPPDATA%\Packages\Microsoft.Limitless_8wekyb3d8bbwe\LocalCache\exe.xml`
(Store) or `%APPDATA%\Microsoft Flight Simulator 2024\exe.xml` (Steam). If
the file doesn't exist, create it like this; otherwise add only the
`<Launch.Addon>` block inside `<SimBase.Document>`:

```xml
<?xml version="1.0" encoding="Windows-1252"?>
<SimBase.Document Type="SimConnect" version="1,0">
  <Descr>Auto launch external applications on MSFS start</Descr>
  <Filename>exe.xml</Filename>
  <Disabled>False</Disabled>
  <Launch.ManualLoad>False</Launch.ManualLoad>
  <Launch.Addon>
    <Name>SkyDemonConnect</Name>
    <Disabled>False</Disabled>
    <Path>C:\Users\you\AppData\Local\Programs\SkyDemonConnect\SkyDemonConnect.exe</Path>
    <CommandLine>-auto</CommandLine>
  </Launch.Addon>
</SimBase.Document>
```

## Demo

`.\SkyDemonConnect.exe -demo` flies a 100 kt circle without MSFS, to test
the connection; quit the tray copy first, as only one runs at a time.
`-demo-lat` and `-demo-lon` move the circle. Add `-test-traffic` for three
made-up aircraft around yours, to see traffic in SkyDemon.

## Traffic

Every aircraft and helicopter MSFS simulates within 30 NM is sent to SkyDemon
once a second, the 50 nearest if there are more, whoever created it: MSFS's
own AI and live traffic, and traffic injectors such as BeyondATC or FSLTL.
Whether MSFS 2024 hands out multiplayer aircraft this way is not verified
yet. The label is the aircraft's ATC ID as MSFS shows it, for airliners
mostly the registration rather than the flight number, or `T` and its
made-up ICAO address without one. Airborne aircraft come first, parked ones
last. Traffic goes over
UDP only, as NMEA on `-com` has no traffic sentences, and so does
`-test-traffic`. `-no-traffic` turns it off.

## SkyDemon routes in the G1000

The route you last saved in SkyDemon on this PC goes to the G1000 by itself,
in any aircraft with Working Title's G1000 NXi, both the MSFS 2024 one and
the one carried over from MSFS 2020, e.g. in the COWS DA40 or the Carenado
C182T:

- **On the ground** within 5 NM of the route's start, the route replaces the
  G1000's flight plan right away: about 10 seconds after the flight has
  started, or as soon as you save it.
- **Elsewhere and in flight**, a route saved during the flight makes the MFD
  ask first: **LOAD** replaces the active flight plan, **KEEP** leaves it, and
  either way the route isn't offered again until it is saved with changes.
  In flight the G1000 then flies to the nearest leg. A route saved before
  the flight, likely one for another day, is only loaded at its start.

SkyDemon saves positions only, no names or airport codes. Each of its
aerodromes becomes the sim's airport within 1 NM, the first and last one the
departure and destination; every other point becomes a user waypoint
`SKD…`. Runways, circuits and procedures aren't carried over.

It works with two parts:

- SkyDemonConnect reads the newest `.flightplan` in `SkyDemon\Routes` in
  your Documents folder, where SkyDemon saves routes on the PC (`-routes`
  for another folder). It hands it out on `http://127.0.0.1:49080/route`,
  to this PC only, so the firewall doesn't ask.
- A plugin for the G1000 asks for it every few seconds. SkyDemonConnect
  installs it as the package `skydemon-connect-plugin` in MSFS 2024's
  Community folder, found through MSFS's `UserCfg.opt`. MSFS loads it at
  its next start; the log (`route: MSFS plugin …`) says what happened.
  Another package of that name, or a link to one, is left alone.

After an update of SkyDemonConnect, the plugin is updated when its files
changed, but not while MSFS runs, as MSFS only reads its packages when it
starts: with **Start with MSFS** that is when MSFS closes, so the new plugin
runs from the next start on. Until then the MFD says to restart MSFS.

A route planned on a tablet reaches the G1000 once it is opened and saved in
SkyDemon on the PC.

`-no-route` turns all of this off and leaves the plugin alone. To remove the
plugin, delete the `skydemon-connect-plugin` folder from the Community folder
while MSFS is closed, and start SkyDemonConnect with `-no-route` from then
on; otherwise it installs the plugin again.

## Update and uninstall

To update, choose **Quit**, extract the new zip into the same folder and
replace the files; if you extract it elsewhere, click **Start with MSFS**
again in the new copy.

To uninstall, untick **Start with MSFS** (if it reads "not set up for this
copy", click it twice: the first click puts this copy's entry in its
place), choose **Quit** and delete the folder and, from MSFS's Community
folder, `skydemon-connect-plugin`. The backups
`exe.xml.bak-skydemonconnect` and `exe.xml.prev-skydemonconnect` next to
MSFS's `exe.xml`, and `%LOCALAPPDATA%\SkyDemonConnect` if it exists, can go
too.

## Options

Add options in PowerShell (`.\SkyDemonConnect.exe -demo`) or to a shortcut's
target; `-help` lists them. Quote comma-separated lists in PowerShell, e.g.
`-udp "127.0.0.1:49002,192.168.1.23:49002"`, and write `-udp=` rather than
`-udp ""`, which PowerShell 5.1 drops.

| Option          | Default | Meaning                                                                              |
| --------------- | ------- | ------------------------------------------------------------------------------------ |
| `-udp`          | `auto`  | where to send the UDP messages, see below                                            |
| `-com`          |         | also write NMEA (RMC, GGA, GSA) to a COM port                                        |
| `-baud`         | 115200  | COM port baud rate (with `-com`)                                                     |
| `-no-xatt`      | off     | don't send attitude messages                                                         |
| `-no-traffic`   | off     | don't send the aircraft around yours                                                 |
| `-no-route`     | off     | don't hand SkyDemon routes to the G1000, nor install its plugin                      |
| `-routes`       |         | full path of the folder SkyDemon saves routes to, if not `Documents\SkyDemon\Routes` |
| `-name`         | MSFS    | simulator name in the messages, up to 32 ASCII characters, no commas                 |
| `-demo`         | off     | fly a 100 kt circle without MSFS                                                     |
| `-demo-lat`     | 51.4239 | centre of the demo circle (Leipzig/Halle), with `-demo`                              |
| `-demo-lon`     | 12.2364 |                                                                                      |
| `-test-traffic` | off     | three made-up aircraft around yours, instead of MSFS's traffic                       |
| `-console`      | off     | a console window with a live status line instead of the tray                         |
| `-quiet`        | off     | with `-console`: no status line                                                      |
| `-auto`         | off     | as used in `exe.xml`: start silently, exit when MSFS closes                          |
| `-version`      |         | show the version                                                                     |

`-udp` takes comma-separated `host` or `host:port` targets (port 49002 if
left out). `auto` is this PC and every local network; `-udp=` turns UDP
off, for use with `-com`.

`-com` is for apps that read a GPS from a serial port: create a virtual
null-modem pair, e.g. COM11 and COM12 with
[com0com](https://com0com.sourceforge.net), run `-com COM11` and point the
app at COM12. A port that isn't there yet, e.g. a USB adapter, is opened
once it is.

## Which networks `auto` covers

`auto` covers every network adapter with IPv4 and broadcast, also e.g.
Hyper-V's or WSL's, public Wi-Fi, and VPNs whose adapter looks like Ethernet
to Windows (most corporate ones); dial-up and tunnel adapters are left out.
To send to one tablet instead, see
[Tablets and phones](../README.md#tablets-and-phones).

## How it works

SkyDemonConnect loads `SimConnect.dll` at runtime (no cgo) and reads the user
aircraft on every MSFS frame. Thirty times a second it sends the newest
position as
[ForeFlight simulator messages](https://foreflight.com/support/network-gps)
(`XGPS` and `XATT` on UDP port 49002) and, with `-com`, as NMEA sentences.
The track is the ground track, including wind drift; all directions are
true. Once a second it asks MSFS for the aircraft and helicopters within
30 NM and sends each but your own as `XTRAFFIC`. The plugin is plain
JavaScript using the avionics' own flight planning code, in a folder per
avionics; it lives in `internal/plugin/package` and is built into the exe.

## Development

The exe is cross-compiled with Go from any OS. `make help` lists the
targets, e.g. `make build`, `make test`, `make lint` and `make demo`. The
Makefile uses the Go of go.mod's `toolchain` line (downloading it if
needed), so that a build gives the same exe everywhere. `make test` uses the
race detector, which needs a C compiler (`make test RACE=` without);
`make lint` needs curl and tar (unzip on Windows); `make verify-format` and
`make format` run Prettier in a node container and need podman (or
`CONTAINER_RUNTIME=docker`); `make bundle` builds the release zip,
downloading the 650 MB MSFS SDK installer, and needs curl, unzip and
`msiextract` from msitools.

Pushing a `v*` tag on `main` runs CI and publishes a release with
`SHA256SUMS` and build provenance, which `gh attestation verify` checks. A
tag like `v1.2.0-rc.1` makes a pre-release, to try the release first. For
notes of your own, save a draft release for the tag before approving the
`release` job; it publishes the draft with the assets. Don't publish or delete
the release by hand: releases are immutable, so a published one takes no
assets, and a deleted one's tag can't be released again. To
bump the MSFS SDK, golangci-lint, Prettier, node or Go, see the comments at the
top of the Makefile and in `.github/workflows/ci.yml`; Dependabot covers the
actions, go-winres and govulncheck. GitHub turns off scheduled workflows
after 60 days without activity in the repository; re-enable the weekly `ci`
run in the Actions tab if it was.
