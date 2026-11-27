# SkyDemonConnect

[![ci](https://github.com/saschagrunert/skydemon-connect/actions/workflows/ci.yml/badge.svg)](https://github.com/saschagrunert/skydemon-connect/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/saschagrunert/skydemon-connect/graph/badge.svg)](https://codecov.io/gh/saschagrunert/skydemon-connect)

Smooth moving map for [SkyDemon](https://www.skydemon.aero) in Microsoft
Flight Simulator 2024 (MSFS). Simulator bridges usually send one position
per second, so the aircraft jumps; SkyDemonConnect sends 30 per second, to
SkyDemon on the same PC and to tablets and phones on your network. It is a
single `.exe` in the system tray that can start and stop with MSFS.

## Install

1. Close any other simulator bridge, so that SkyDemon gets one feed only.
2. Download `SkyDemonConnect-<version>.zip` from the
   [releases](https://github.com/saschagrunert/skydemon-connect/releases).
   To check that it is the one this repository built, run
   `gh attestation verify SkyDemonConnect-<version>.zip -R saschagrunert/skydemon-connect`
   ([GitHub CLI](https://cli.github.com)). `SHA256SUMS` from the same release
   (compare with `Get-FileHash` in PowerShell) only shows that the download
   isn't broken. Once it checks out, you may right-click the zip →
   Properties → Unblock, so that SmartScreen doesn't ask later.
3. Extract it (right-click → Extract All) to a folder of its own, e.g.
   `%LOCALAPPDATA%\Programs\SkyDemonConnect\`, not into Downloads. A folder
   of your own profile is safer than one directly under `C:\`, where other
   users of the PC can change files. The zip contains `SimConnect.dll` from
   the MSFS 2024 SDK; see [NOTICE.md](NOTICE.md).
4. Start `SkyDemonConnect.exe`. It is not signed, so SmartScreen may ask
   first (More info → Run anyway). Smart App Control on Windows 11 blocks
   unsigned apps; the only way around it is to turn it off (Windows Security
   → App & browser control). Recent Windows 11 updates can turn it back on
   there, older ones only by reinstalling Windows. If Windows Firewall asks,
   either answer works: SkyDemonConnect only sends.
5. In SkyDemon, on the PC or a tablet: **Go Flying → Connect to X-Plane**.
6. Check it works: start a flight in MSFS. The tray icon turns green, its
   tooltip shows altitude, speed and track, and SkyDemon shows the aircraft
   moving. Without MSFS, try the [demo](#demo).

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
`-quiet` or `-version`, which only make sense for one start.

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

## Tablets and phones

By default (`-udp auto`) SkyDemonConnect sends to this PC and to each local
network's broadcast address, e.g. `192.168.1.255`, so a tablet on the same
Wi-Fi gets it without any setup.

If your router doesn't pass broadcasts to Wi-Fi, or to send to one tablet
only, give its address together with this PC's:
`-udp "127.0.0.1:49002,192.168.1.23:49002"`. Don't add the tablet's address
to `auto`: the tablet then gets every position twice, and as access points
hold broadcasts back for devices that save power, the broadcast copy can
come up to 300 ms late. The messages carry no time, so the aircraft jumps
back. Host names such as `ipad.local:49002` work too and are looked up again
every 10 seconds.

`auto` covers every network adapter with IPv4 and broadcast, also e.g.
Hyper-V's or WSL's, public Wi-Fi, and VPNs whose adapter looks like Ethernet
to Windows (most corporate ones); dial-up and tunnel adapters are left out.

## Demo

`.\SkyDemonConnect.exe -demo` flies a 100 kt circle without MSFS, to test
the connection; quit the tray copy first, as only one runs at a time.
`-demo-lat` and `-demo-lon` move the circle.

## Update and uninstall

To update, choose **Quit**, extract the new zip into the same folder and
replace the files; if you extract it elsewhere, click **Start with MSFS**
again in the new copy.

To uninstall, untick **Start with MSFS** (if it reads "not set up for this
copy", click it twice: the first click puts this copy's entry in its place), choose
**Quit** and delete the folder. The backups `exe.xml.bak-skydemonconnect`
and `exe.xml.prev-skydemonconnect` next to MSFS's `exe.xml`, and
`%LOCALAPPDATA%\SkyDemonConnect` if it exists, can go too.

## Troubleshooting

**Open log** shows where positions go and what happened with MSFS; attach
`SkyDemonConnect.log` when reporting a problem.

- **`SimConnect.dll` not found:** keep it next to the exe, as in the zip, and
  don't run the exe from inside the zip. Without one there, the SDK folder
  named by the `MSFS2024_SDK` or `MSFS_SDK` environment variable, which the
  SDK installer sets, is tried; a DLL next to the exe is always the one
  used.
- **`SimConnect.dll` can't be loaded:** install the
  [Microsoft Visual C++ runtime](https://aka.ms/vs/17/release/vc_redist.x64.exe),
  or replace a 32-bit copy from FSX with the one from the zip.
- **The tablet gets nothing:** it must be on the same network as the PC, not
  a guest Wi-Fi, and without a VPN in the way. Some routers don't pass
  broadcasts to Wi-Fi: send to the tablet directly, see
  [Tablets and phones](#tablets-and-phones).
- **The icon stays grey in a flight:** see **Open log**. If MSFS runs as
  administrator, use **Start with MSFS**, so that MSFS starts it the same way.
- **The map still jumps:** another simulator bridge is still running, or the
  tablet gets the positions twice, see
  [Tablets and phones](#tablets-and-phones).

## Options

Add options in PowerShell (`.\SkyDemonConnect.exe -demo`) or to a shortcut's
target; `-help` lists them. Quote comma-separated lists in PowerShell, e.g.
`-udp "127.0.0.1:49002,192.168.1.23:49002"`, and write `-udp=` rather than
`-udp ""`, which PowerShell 5.1 drops.

| Option      | Default | Meaning                                                              |
| ----------- | ------- | -------------------------------------------------------------------- |
| `-udp`      | `auto`  | comma-separated `host` or `host:port` targets (port 49002 if left out); `auto` is this PC and every local network; `-udp=` turns UDP off (with `-com`) |
| `-com`      |         | also write NMEA (RMC, GGA, GSA) to a COM port                        |
| `-baud`     | 115200  | COM port baud rate (with `-com`)                                     |
| `-no-xatt`  | off     | don't send attitude messages                                         |
| `-name`     | MSFS    | simulator name in the messages, up to 32 ASCII characters, no commas |
| `-demo`     | off     | fly a 100 kt circle without MSFS                                     |
| `-demo-lat` | 51.4239 | centre of the demo circle (Leipzig/Halle), with `-demo`              |
| `-demo-lon` | 12.2364 |                                                                      |
| `-console`  | off     | a console window with a live status line instead of the tray         |
| `-quiet`    | off     | with `-console`: no status line                                      |
| `-auto`     | off     | as used in `exe.xml`: start silently, exit when MSFS closes          |
| `-version`  |         | show the version                                                     |

`-com` is for apps that read a GPS from a serial port: create a virtual
null-modem pair, e.g. COM11 and COM12 with
[com0com](https://com0com.sourceforge.net), run `-com COM11` and point the
app at COM12. A port that isn't there yet, e.g. a USB adapter, is opened
once it is.

## Advanced

### How it works

SkyDemonConnect loads `SimConnect.dll` at runtime (no cgo) and reads the user
aircraft on every MSFS frame. Thirty times a second it sends the newest
position as
[ForeFlight simulator messages](https://foreflight.com/support/network-gps)
(`XGPS` and `XATT` on UDP port 49002) and, with `-com`, as NMEA sentences.
The track is the ground track, including wind drift; all directions are
true.

### Development

The exe is cross-compiled with Go from any OS. `make help` lists the targets,
e.g. `make build`, `make test`, `make lint` and `make demo`. The Makefile
uses the Go of go.mod's `toolchain` line (downloading it if needed), so that
a build gives the same exe everywhere. `make test` uses the race detector,
which needs a C compiler (`make test RACE=` without); `make lint` needs curl
and tar (unzip on Windows); `make bundle` builds the release zip,
downloading the 650 MB MSFS SDK installer, and needs curl, unzip and
`msiextract` from msitools.

Pushing a `v*` tag on `main` runs CI and publishes a release with
`SHA256SUMS` and build provenance, which `gh attestation verify` checks. A
tag like `v1.2.0-rc.1` makes a pre-release, to try the release first. To
bump the MSFS SDK, golangci-lint or Go, see the comments at the top of the
Makefile and in `.github/workflows/ci.yml`; Dependabot covers the actions,
go-winres and govulncheck. GitHub turns off scheduled workflows after 60
days without activity in the repository; re-enable the weekly `ci` run in
the Actions tab if it was.

## License

[Apache License 2.0](LICENSE). SkyDemonConnect is not affiliated with or
endorsed by SkyDemon (Divelements Ltd), ForeFlight or Microsoft.
