# SkyDemonConnect

[![ci](https://github.com/saschagrunert/skydemon-connect/actions/workflows/ci.yml/badge.svg)](https://github.com/saschagrunert/skydemon-connect/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/saschagrunert/skydemon-connect/graph/badge.svg)](https://codecov.io/gh/saschagrunert/skydemon-connect)

Smooth moving map for [SkyDemon](https://www.skydemon.aero) in Microsoft
Flight Simulator 2024 (MSFS). Simulator bridges usually send one position
per second, so the aircraft jumps; SkyDemonConnect sends 30 per second, to
SkyDemon on the same PC and to tablets and phones on your network. It is a
single `.exe` in the system tray that can start and stop with MSFS. Other
aircraft around you show up as traffic, too, and the route you save in
SkyDemon [lands in the G1000](docs/reference.md#skydemon-routes-in-the-g1000).

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
   either answer works: SkyDemonConnect only sends, and listens only for the
   G1000 on this PC.
5. In SkyDemon, on the PC or a tablet: **Go Flying → Connect to X-Plane**.
6. Check it works: start a flight in MSFS. The tray icon turns green, its
   tooltip shows altitude, speed and track, and SkyDemon shows the aircraft
   moving. Without MSFS, try the [demo](docs/reference.md#demo).
7. To have MSFS start and stop it, tick **Start with MSFS** in the tray
   menu.

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

To use an option, choose **Quit** and start the exe with it in PowerShell
(`.\SkyDemonConnect.exe -udp "..."`) or a shortcut's target. If MSFS starts
SkyDemonConnect too, the menu of the copy you just started reads "Start with
MSFS (not set up for this copy, click to fix)": click it there, so that MSFS
starts it with the option as well. If the item is ticked instead, the entry
already has the option; leave it alone, as a click removes the entry.

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

The tray menu, starting with MSFS (and adding it to MSFS's `exe.xml` by
hand), the demo, traffic, SkyDemon routes in the G1000, all options,
updating and uninstalling are described in
[docs/reference.md](docs/reference.md).

## License

[Apache License 2.0](LICENSE). SkyDemonConnect is not affiliated with or
endorsed by SkyDemon (Divelements Ltd), ForeFlight or Microsoft.
