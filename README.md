# Loadout

A Windows app for organising a PS4/PS5 game library on your PC and sending games to a PS5.

- Scans your game folders, names them consistently and keeps a library with icons, versions and DLC
- Sends games to the PS5 over the network (using the bundled [PS5Upload](https://github.com/phantomptr/ps5upload) engine)
- Builds exFAT drive images, manages saves and cheats, and has a phone remote page
- Keeps the PC awake during long jobs and runs from the tray

## Install

Download the latest `Loadout-Setup-x.y.z.exe` from [Releases](../../releases) and run it.
Requires Windows 10/11 with the Microsoft Edge WebView2 Runtime (included with Windows 11).

## Build from source

Needs [Go](https://go.dev/dl/) and [NSIS](https://nsis.sourceforge.io/). Then run:

```powershell
.\build.ps1
```

When releasing, bump the version in both `app.go` (`appVersion`) and `installer.nsi` (`VERSION`).

## Third-party

`ps5upload-engine.exe` is PS5Upload v6.1.0 by PhantomPtr and contributors, unchanged, licensed under GPLv3.
See `PS5Upload-NOTICE.txt` and `PS5Upload-LICENSE.txt`.
