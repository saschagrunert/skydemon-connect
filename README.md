# SkyDemonConnect

[![ci](https://github.com/saschagrunert/skydemon-connect/actions/workflows/ci.yml/badge.svg)](https://github.com/saschagrunert/skydemon-connect/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/saschagrunert/skydemon-connect/graph/badge.svg)](https://codecov.io/gh/saschagrunert/skydemon-connect)

Smooth moving map for [SkyDemon](https://www.skydemon.aero) in Microsoft
Flight Simulator 2024 (MSFS). Simulator bridges usually send one position
per second, so the aircraft jumps; SkyDemonConnect sends 30 per second, to
SkyDemon on the same PC and to tablets and phones on your network. It is a
single `.exe` in the system tray that can start and stop with MSFS.

## Quick start

1. Close any other simulator bridge, so that SkyDemon gets one feed only.
2. Download `SkyDemonConnect-<version>.zip` from the
   [releases](https://github.com/saschagrunert/skydemon-connect/releases) and
   extract it (right-click → Extract All) to a folder of its own, e.g.
   `%LOCALAPPDATA%\Programs\SkyDemonConnect\`, not into Downloads. A folder
   of your own profile is safer than one directly under `C:\`, where other
   users of the PC can change files. The zip contains `SimConnect.dll` from
   the MSFS 2024 SDK; see [NOTICE.md](NOTICE.md). To check the download
   first, compare `Get-FileHash SkyDemonConnect-<version>.zip` in PowerShell
   with `SHA256SUMS` from the same release, or run
   `gh attestation verify SkyDemonConnect-<version>.zip -R saschagrunert/skydemon-connect`.
3. Start `SkyDemonConnect.exe`. It is not signed, so SmartScreen asks first
   (More info → Run anyway; or, once the zip checked out, right-click it →
   Properties → Unblock before extracting). Smart App Control on Windows 11
   blocks unsigned apps, and the only way around it is to turn it off
   (Windows Security → App & browser control); on most PCs it can't be
   turned back on without reinstalling Windows. If Windows Firewall asks,
   either answer works: SkyDemonConnect only sends.
4. In SkyDemon, on the PC or a tablet: **Go Flying → Connect to X-Plane**.

`.\SkyDemonConnect.exe -demo` flies a circle without MSFS, to test the
connection; quit the tray copy first, as only one runs at a time.

To update, choose **Quit**, extract the new zip into the same folder and
replace the files; if you extract it elsewhere, tick **Start with MSFS** again
from the new copy.

To uninstall, untick **Start with MSFS** (if it reads "another or disabled
entry", click it twice: the first click puts this copy's entry in its
place), choose **Quit** and delete the folder; the backups
`exe.xml.bak-skydemonconnect` and `exe.xml.prev-skydemonconnect` next to
MSFS's `exe.xml` and
`%LOCALAPPDATA%\SkyDemonConnect` (if it exists) can go too.

## Tray icon

| Icon | Meaning                                                     |
| ---- | ----------------------------------------------------------- |
| 🟢   | sending positions; hover for altitude, speed and track      |
| ⚪   | waiting for MSFS: not running, loading or in its menu       |
| 🔴   | a problem, e.g. `SimConnect.dll` missing; hover for details |

The menu shows the status and where positions go (UDP addresses, the COM
port), then **Start with MSFS**, **Open log** and **Quit**. Only one copy
runs at a time. Each start writes a fresh `SkyDemonConnect.log` next to the
exe (or in `%LOCALAPPDATA%\SkyDemonConnect` if that folder is read-only) and
keeps the previous one as `SkyDemonConnect.previous.log`; `-console` writes
none. A copy run from the same folder in another Windows session logs to
`SkyDemonConnect.<number>.log` instead, which is deleted after a week.

In MSFS's menus no position is sent and SkyDemon shows the GPS as lost.
During an active pause (the Pause key) the last position keeps being sent,
standing still once MSFS's frames stop changing. Whether MSFS 2024 counts its Esc menu as a
menu or as a pause is not verified yet; the log says which (`simconnect: no
flight running` or nothing).

**Start with MSFS** adds the exe to MSFS's `exe.xml` (Microsoft Store and
Steam), so that MSFS starts it and it quits with MSFS. It passes on the
options this copy was started with that differ from the defaults, e.g.
`-udp`. The original file is backed up once to
`exe.xml.bak-skydemonconnect`, and the file as it was before the latest
change to `exe.xml.prev-skydemonconnect`. After moving the exe or to change the options,
start it the new way: the menu item then reads "Start with MSFS (another or
disabled entry)", and a click replaces the entry with this copy's. With
`-auto`, MSFS started it and it exits when MSFS exits; if MSFS isn't its
parent process, it exits when MSFS's SimConnect session ends, and keeps
running if MSFS closes before one ever started. To add it by hand, put this
inside `<SimBase.Document>` of
`%LOCALAPPDATA%\Packages\Microsoft.Limitless_8wekyb3d8bbwe\LocalCache\exe.xml`
(Store) or `%APPDATA%\Microsoft Flight Simulator 2024\exe.xml` (Steam):

```xml
<Launch.Addon>
  <Name>SkyDemonConnect</Name>
  <Disabled>False</Disabled>
  <Path>C:\Users\you\AppData\Local\Programs\SkyDemonConnect\SkyDemonConnect.exe</Path>
  <CommandLine>-auto</CommandLine>
</Launch.Addon>
```

## Options

Add options in PowerShell (`.\SkyDemonConnect.exe -demo`) or to a shortcut's
target; `-help` lists them. Quote comma-separated lists in PowerShell, e.g.
`-udp "auto,192.168.1.23:49002"`.

| Option      | Default | Meaning                                                              |
| ----------- | ------- | -------------------------------------------------------------------- |
| `-udp`      | `auto`  | comma-separated `host:port` targets; `auto` is this PC and every local network; `-udp=` turns UDP off (with `-com`) |
| `-com`      |         | also write NMEA (RMC, GGA, GSA) to a COM port                        |
| `-baud`     | 115200  | COM port baud rate                                                   |
| `-no-xatt`  | off     | don't send attitude messages                                         |
| `-name`     | MSFS    | simulator name in the messages, up to 32 ASCII characters, no commas |
| `-demo`     | off     | fly a 100 kt circle without MSFS                                     |
| `-demo-lat` | 51.4239 | centre of the demo circle (Leipzig/Halle)                            |
| `-demo-lon` | 12.2364 |                                                                      |
| `-console`  | off     | a console window with a live status line instead of the tray         |
| `-quiet`    | off     | with `-console`: no status line                                      |
| `-auto`     | off     | as used in `exe.xml`: start silently, exit when MSFS closes          |
| `-version`  |         | show the version                                                     |

`-com` is for apps that read a GPS from a serial port: create a virtual
null-modem pair, e.g. COM11 and COM12 with
[com0com](https://com0com.sourceforge.net), run `-com COM11` and point the
app at COM12. A port that isn't there yet, e.g. a USB adapter, is opened
once it is. In PowerShell 5.1, write `-udp=` rather than `-udp ""`, which it
drops.

`auto` sends to each network's broadcast address (e.g. `192.168.1.255`) and
follows network changes. That is every network adapter with IPv4 and
broadcast, also e.g. Hyper-V's or WSL's, public Wi-Fi, and VPNs whose adapter
looks like Ethernet to Windows (most corporate ones); dial-up and tunnel
adapters are left out. To send only to your tablet, give its
address instead, e.g. `-udp 192.168.1.23:49002`. Host names such as `ipad.local:49002` are looked up
again every 10 seconds.

## Troubleshooting

**Open log** shows where positions go and what happened with MSFS; attach
`SkyDemonConnect.log` when reporting a problem.

- **`SimConnect.dll` not found:** keep it next to the exe, as in the zip, and
  don't run the exe from inside the zip. It is also found in the SDK folder
  named by the `MSFS2024_SDK` or `MSFS_SDK` environment variable, which the
  SDK installer sets. That folder defaults to `C:\MSFS 2024 SDK`, where other
  users of the PC can change files; on a shared PC, keep the DLL next to the
  exe, which is looked at first. If the log says it can't be loaded, install
  the Microsoft Visual C++ runtime (`vc_redist.x64.exe`), or replace a 32-bit
  copy from FSX with the MSFS 2024 one.
- **The tablet gets nothing:** it must be on the same network as the PC, not
  a guest Wi-Fi, and without a VPN in the way. Some routers don't pass
  broadcasts to Wi-Fi: send to the tablet directly, e.g.
  `-udp "auto,192.168.1.23:49002"`.
- **The icon stays grey in a flight:** see **Open log**. If MSFS runs as
  administrator, use **Start with MSFS**, so that MSFS starts it the same way.
- **The map still jumps:** another simulator bridge is still running.

## How it works

SkyDemonConnect loads `SimConnect.dll` at runtime (no cgo) and reads the user
aircraft on every MSFS frame. Thirty times a second it sends the newest
position as
[ForeFlight simulator messages](https://foreflight.com/support/network-gps)
(`XGPS` and `XATT` on UDP port 49002) and, with `-com`, as NMEA sentences.
The track is the ground track, including wind drift; all directions are
true.

## Development

The exe is cross-compiled with Go from any OS. `make help` lists the targets,
e.g. `make build`, `make test`, `make lint` and `make demo`. `make test` uses
the race detector, which needs a C compiler (`make test RACE=` without);
`make lint` needs curl and tar (unzip on Windows); `make bundle` builds the release zip,
downloading the 650 MB MSFS SDK installer, and needs curl, unzip and
`msiextract` from msitools.

Pushing a `v*` tag on `main` runs CI and publishes a release with
`SHA256SUMS` and build provenance, which
`gh attestation verify SkyDemonConnect.exe -R saschagrunert/skydemon-connect`
checks. A tag like `v1.2.0-rc.1` makes a pre-release, to try the release
first. To bump the MSFS SDK, golangci-lint or Go, see the comments at the top
of the Makefile and in `.github/workflows/ci.yml`; Dependabot covers the
actions, go-winres and govulncheck. GitHub turns off scheduled workflows
after 60 days without activity in the repository; re-enable the weekly
`ci` run in the Actions tab if it was.

## License

[Apache License 2.0](LICENSE). SkyDemonConnect is not affiliated with or
endorsed by SkyDemon (Divelements Ltd), ForeFlight or Microsoft.
