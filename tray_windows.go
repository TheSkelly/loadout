//go:build windows

package main

import (
	"os"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// Close-to-tray: closing the window hides it and leaves Loadout running next
// to the clock, so builds, sends and the phone page keep going. The tray
// icon's menu has the real Quit.

const hasTray = true

var (
	pSetWindowLongPtrW          = user32.NewProc("SetWindowLongPtrW")
	pCallWindowProcW            = user32.NewProc("CallWindowProcW")
	pShowWindow                 = user32.NewProc("ShowWindow")
	pSetForegroundWindow        = user32.NewProc("SetForegroundWindow")
	pIsIconic                   = user32.NewProc("IsIconic")
	pLoadImageW                 = user32.NewProc("LoadImageW")
	pCreatePopupMenu            = user32.NewProc("CreatePopupMenu")
	pAppendMenuW                = user32.NewProc("AppendMenuW")
	pTrackPopupMenu             = user32.NewProc("TrackPopupMenu")
	pDestroyMenu                = user32.NewProc("DestroyMenu")
	pGetCursorPos               = user32.NewProc("GetCursorPos")
	pPostMessageW               = user32.NewProc("PostMessageW")
	pFindWindowW                = user32.NewProc("FindWindowW")
	pRegisterWindowMsgW         = user32.NewProc("RegisterWindowMessageW")
	pShellNotifyIconW           = shell32.NewProc("Shell_NotifyIconW")
	pGetModuleHandleW           = kernel32.NewProc("GetModuleHandleW")
	pCreateMutexW               = kernel32.NewProc("CreateMutexW")
	pSetThreadExecState         = kernel32.NewProc("SetThreadExecutionState")
	closeToTray           int32 = 1
	reallyQuit            int32
	trayHwnd, trayOldProc uintptr
	trayShown, trayTold   bool
	msgTaskbarCreated     uint32
)

const (
	wmClose      = 0x0010
	wmDestroy    = 0x0002
	wmCommand    = 0x0111
	wmSysCommand = 0x0112
	wmApp        = 0x8000
	wmTray       = wmApp + 61
	wmShowMe     = wmApp + 62
	wmLButtonUp  = 0x0202
	wmLButtonDbl = 0x0203
	wmRButtonUp  = 0x0205
	wmContext    = 0x007B
	swHide       = 0
	swShow       = 5
	swRestore    = 9
	nimAdd       = 0
	nimModify    = 1
	nimDelete    = 2
	nifMessage   = 1
	nifIcon      = 2
	nifTip       = 4
	nifInfo      = 0x10
	idOpen       = 1001
	idQuit       = 1002
)

type notifyIconData struct {
	cbSize           uint32
	hWnd             uintptr
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            uintptr
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uVersion         uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         [16]byte
	hBalloonIcon     uintptr
}

func setCloseToTray(on bool) {
	v := int32(0)
	if on {
		v = 1
	}
	atomic.StoreInt32(&closeToTray, v)
}

func copyU16(dst []uint16, s string) {
	u, _ := syscall.UTF16FromString(s)
	if len(u) > len(dst) {
		u = u[:len(dst)]
		u[len(u)-1] = 0
	}
	copy(dst, u)
}

func trayIcon(action uintptr, balloon string) {
	var nid notifyIconData
	nid.cbSize = uint32(unsafe.Sizeof(nid))
	nid.hWnd, nid.uID = trayHwnd, 1
	nid.uFlags = nifMessage | nifIcon | nifTip
	nid.uCallbackMessage = wmTray
	nid.hIcon = appSmallIcon()
	copyU16(nid.szTip[:], "Loadout")
	if balloon != "" {
		nid.uFlags |= nifInfo
		copyU16(nid.szInfoTitle[:], "Loadout is still running")
		copyU16(nid.szInfo[:], balloon)
		nid.dwInfoFlags = 1 // NIIF_INFO
	}
	pShellNotifyIconW.Call(action, uintptr(unsafe.Pointer(&nid)))
}

func showMainWindow() {
	if iconic, _, _ := pIsIconic.Call(trayHwnd); iconic != 0 {
		pShowWindow.Call(trayHwnd, swRestore)
	} else {
		pShowWindow.Call(trayHwnd, swShow)
	}
	pSetForegroundWindow.Call(trayHwnd)
}

func trayMenu() {
	menu, _, _ := pCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	open, _ := syscall.UTF16PtrFromString("Open Loadout")
	quit, _ := syscall.UTF16PtrFromString("Quit Loadout")
	pAppendMenuW.Call(menu, 0, idOpen, uintptr(unsafe.Pointer(open)))
	pAppendMenuW.Call(menu, 0x800 /*MF_SEPARATOR*/, 0, 0)
	pAppendMenuW.Call(menu, 0, idQuit, uintptr(unsafe.Pointer(quit)))
	var pt struct{ x, y int32 }
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	pSetForegroundWindow.Call(trayHwnd) // so the menu closes when you click away
	cmd, _, _ := pTrackPopupMenu.Call(menu, 0x0100 /*TPM_RETURNCMD*/ |0x0020 /*TPM_BOTTOMALIGN*/, uintptr(pt.x), uintptr(pt.y), 0, trayHwnd, 0)
	pDestroyMenu.Call(menu)
	switch cmd {
	case idOpen:
		showMainWindow()
	case idQuit:
		atomic.StoreInt32(&reallyQuit, 1)
		pPostMessageW.Call(trayHwnd, wmClose, 0, 0)
	}
}

func trayWndProc(hwnd, msg, wp, lp uintptr) uintptr {
	switch uint32(msg) {
	case wmClose:
		if atomic.LoadInt32(&closeToTray) != 0 && atomic.LoadInt32(&reallyQuit) == 0 {
			pShowWindow.Call(hwnd, swHide)
			if !trayTold {
				trayTold = true
				trayIcon(nimModify, "It's in the tray next to the clock. Right-click the icon and choose Quit to close it for real.")
			}
			return 0
		}
	case wmDestroy:
		trayIcon(nimDelete, "")
	case wmTray:
		switch uint32(lp) & 0xFFFF {
		case wmLButtonUp, wmLButtonDbl:
			showMainWindow()
		case wmRButtonUp, wmContext:
			trayMenu()
		}
		return 0
	case wmShowMe:
		showMainWindow()
		return 0
	}
	if msgTaskbarCreated != 0 && uint32(msg) == msgTaskbarCreated {
		trayIcon(nimAdd, "") // Explorer restarted: put the icon back
	}
	r, _, _ := pCallWindowProcW.Call(trayOldProc, hwnd, msg, wp, lp)
	return r
}

// installTray adds the tray icon and takes over the window's close button.
func installTray(hwnd uintptr) {
	trayHwnd = hwnd
	name, _ := syscall.UTF16PtrFromString("TaskbarCreated")
	m, _, _ := pRegisterWindowMsgW.Call(uintptr(unsafe.Pointer(name)))
	msgTaskbarCreated = uint32(m)
	gwlpWndProc := -4
	trayOldProc, _, _ = pSetWindowLongPtrW.Call(hwnd, uintptr(gwlpWndProc), syscall.NewCallback(trayWndProc))
	trayIcon(nimAdd, "")
	trayShown = true
}

// alreadyRunning brings an existing Loadout window forward. Returns true if
// another copy owns the app (so this one should exit).
func alreadyRunning() bool {
	name, _ := syscall.UTF16PtrFromString("Local\\Loadout.SingleInstance")
	_, _, err := pCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if err != syscall.Errno(183) { // ERROR_ALREADY_EXISTS
		return false
	}
	// Find the other copy's window whatever version it is. (Looking for this
	// version's exact title missed an older copy sitting in the tray, and
	// starting Loadout then did nothing at all.)
	var hwnd uintptr
	var title string
	for i := 0; i < 20 && hwnd == 0; i++ {
		hwnd, title = findLoadoutWindow()
		if hwnd == 0 {
			time.Sleep(150 * time.Millisecond)
		}
	}
	if hwnd == 0 {
		messageBox("Loadout is already running, but its window can't be found.\n\nOpen Task Manager, end \"Loadout.exe\", then start Loadout again.", "Loadout", 0x30)
		return true
	}
	if title == "Loadout "+appVersion {
		pPostMessageW.Call(hwnd, wmShowMe, 0, 0)
		return true
	}
	// a different version is still running: offer to swap it for this one
	if messageBox("An older copy ("+title+") is still running in the tray.\n\nClose it and open Loadout "+appVersion+"? Anything it is building or sending right now will stop.", "Loadout", 0x24) != 6 { // Yes/No, question; 6 = Yes
		pPostMessageW.Call(hwnd, wmShowMe, 0, 0)
		return true
	}
	var pid uint32
	user32.NewProc("GetWindowThreadProcessId").Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid != 0 {
		if h, _, _ := kernel32.NewProc("OpenProcess").Call(0x0001|0x00100000 /*TERMINATE|SYNCHRONIZE*/, 0, uintptr(pid)); h != 0 {
			kernel32.NewProc("TerminateProcess").Call(h, 0)
			kernel32.NewProc("WaitForSingleObject").Call(h, 5000)
			kernel32.NewProc("CloseHandle").Call(h)
			return false // carry on and start this copy
		}
	}
	messageBox("The older copy couldn't be closed. Right-click its tray icon and choose Quit, then start Loadout again.", "Loadout", 0x30)
	return true
}

// findLoadoutWindow returns the main window of a running Loadout, any version.
func findLoadoutWindow() (uintptr, string) {
	var found uintptr
	var foundTitle string
	getText := user32.NewProc("GetWindowTextW")
	cb := syscall.NewCallback(func(h, _ uintptr) uintptr {
		var buf [128]uint16
		n, _, _ := getText.Call(h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if n == 0 {
			return 1
		}
		s := syscall.UTF16ToString(buf[:n])
		if len(s) > 8 && s[:8] == "Loadout " && s[8] >= '0' && s[8] <= '9' {
			found, foundTitle = h, s
			return 0 // stop
		}
		return 1
	})
	user32.NewProc("EnumWindows").Call(cb, 0)
	return found, foundTitle
}

var smallIcon uintptr

// appSmallIcon is the app's own icon at tray size, taken from the EXE itself
// so it doesn't depend on which resource number the icon ended up with.
func appSmallIcon() uintptr {
	if smallIcon != 0 {
		return smallIcon
	}
	if exe, err := os.Executable(); err == nil {
		if p, err := syscall.UTF16PtrFromString(exe); err == nil {
			var small uintptr
			shell32.NewProc("ExtractIconExW").Call(uintptr(unsafe.Pointer(p)), 0, 0, uintptr(unsafe.Pointer(&small)), 1)
			smallIcon = small
		}
	}
	if smallIcon == 0 {
		hinst, _, _ := pGetModuleHandleW.Call(0)
		for id := uintptr(1); id <= 16 && smallIcon == 0; id++ {
			smallIcon, _, _ = pLoadImageW.Call(hinst, id, 1 /*IMAGE_ICON*/, 16, 16, 0)
		}
	}
	if smallIcon == 0 {
		smallIcon, _, _ = user32.NewProc("LoadIconW").Call(0, 32512) // the stock app icon beats a blank square
	}
	return smallIcon
}
