//go:build windows

package main

import (
	"errors"
	"fmt"
	"net"
	"os/exec"

	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
)

var (
	ole32    = syscall.NewLazyDLL("ole32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	dwmapi   = syscall.NewLazyDLL("dwmapi.dll")

	pCoCreateInstance           = ole32.NewProc("CoCreateInstance")
	pCoTaskMemFree              = ole32.NewProc("CoTaskMemFree")
	pSHCreateItemFromParsed     = shell32.NewProc("SHCreateItemFromParsingName")
	pShellExecuteW              = shell32.NewProc("ShellExecuteW")
	pMessageBoxW                = user32.NewProc("MessageBoxW")
	pOpenClipboard              = user32.NewProc("OpenClipboard")
	pEmptyClipboard             = user32.NewProc("EmptyClipboard")
	pSetClipboardData           = user32.NewProc("SetClipboardData")
	pCloseClipboard             = user32.NewProc("CloseClipboard")
	pGlobalAlloc                = kernel32.NewProc("GlobalAlloc")
	pGlobalLock                 = kernel32.NewProc("GlobalLock")
	pGlobalUnlock               = kernel32.NewProc("GlobalUnlock")
	pGetDiskFreeSpaceExW        = kernel32.NewProc("GetDiskFreeSpaceExW")
	pDwmSetWindowAttribute      = dwmapi.NewProc("DwmSetWindowAttribute")
	pSHFileOperationW           = shell32.NewProc("SHFileOperationW")
	pILCreateFromPathW          = shell32.NewProc("ILCreateFromPathW")
	pILFree                     = shell32.NewProc("ILFree")
	pSHOpenFolderAndSelectItems = shell32.NewProc("SHOpenFolderAndSelectItems")
)

var mainView webview2.WebView

// ---------------- small helpers ----------------

func diskFree(path string) int64 {
	for path != "" {
		if st, err := os.Stat(path); err == nil && st.IsDir() {
			break
		}
		parent := filepath.Dir(path)
		if parent == path {
			return -1
		}
		path = parent
	}
	if path == "" {
		return -1
	}
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return -1
	}
	var free, total, totalFree uint64
	r, _, _ := pGetDiskFreeSpaceExW.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&free)),
		uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&totalFree)))
	if r == 0 {
		return -1
	}
	return int64(free)
}

// openPath shows a folder in Explorer, or with sel, opens its parent with
// the item selected. Done through the shell (not by starting explorer.exe)
// so paths with spaces, brackets or commas always work.
func openPath(p string, sel bool) error {
	p = filepath.Clean(p)
	if _, err := os.Stat(p); err != nil {
		return fmt.Errorf("That location isn't there anymore: %s", p)
	}
	_, err := onUI(func() (string, error) {
		u, _ := syscall.UTF16PtrFromString(p)
		if sel {
			pidl, _, _ := pILCreateFromPathW.Call(uintptr(unsafe.Pointer(u)))
			if pidl != 0 {
				hr, _, _ := pSHOpenFolderAndSelectItems.Call(pidl, 0, 0, 0)
				pILFree.Call(pidl)
				if hr == 0 {
					return "", nil
				}
			}
			p = filepath.Dir(p)
			u, _ = syscall.UTF16PtrFromString(p)
		}
		var owner uintptr
		if mainView != nil {
			owner = uintptr(mainView.Window())
		}
		op, _ := syscall.UTF16PtrFromString("open")
		r, _, _ := pShellExecuteW.Call(owner, uintptr(unsafe.Pointer(op)), uintptr(unsafe.Pointer(u)), 0, 0, 1 /*SW_SHOWNORMAL*/)
		if r <= 32 {
			return "", fmt.Errorf("Windows couldn't open %s (code %d)", p, r)
		}
		return "", nil
	})
	return err
}

func copyText(t string) error {
	u := syscall.StringToUTF16(t)
	var hwnd uintptr
	if mainView != nil {
		hwnd = uintptr(mainView.Window())
	}
	if r, _, _ := pOpenClipboard.Call(hwnd); r == 0 {
		return errors.New("Couldn't open the clipboard.")
	}
	defer pCloseClipboard.Call()
	pEmptyClipboard.Call()
	h, _, _ := pGlobalAlloc.Call(0x0002, uintptr(len(u)*2)) // GMEM_MOVEABLE
	if h == 0 {
		return errors.New("Out of memory.")
	}
	ptr, _, _ := pGlobalLock.Call(h)
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(ptr)), len(u)), u)
	pGlobalUnlock.Call(h)
	pSetClipboardData.Call(13, h) // CF_UNICODETEXT
	return nil
}

func messageBox(text, title string, flags uintptr) int {
	t, _ := syscall.UTF16PtrFromString(text)
	c, _ := syscall.UTF16PtrFromString(title)
	var hwnd uintptr
	if mainView != nil {
		hwnd = uintptr(mainView.Window())
	}
	r, _, _ := pMessageBoxW.Call(hwnd, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)), flags)
	return int(r)
}

// ---------------- modern file dialogs (IFileDialog) ----------------

type guid struct {
	d1     uint32
	d2, d3 uint16
	d4     [8]byte
}

func mkguid(s string) guid {
	s = strings.Trim(s, "{}")
	var g guid
	h := func(x string) uint64 {
		var v uint64
		for _, c := range x {
			v <<= 4
			switch {
			case c >= '0' && c <= '9':
				v |= uint64(c - '0')
			case c >= 'a' && c <= 'f':
				v |= uint64(c-'a') + 10
			case c >= 'A' && c <= 'F':
				v |= uint64(c-'A') + 10
			}
		}
		return v
	}
	p := strings.Split(s, "-")
	g.d1, g.d2, g.d3 = uint32(h(p[0])), uint16(h(p[1])), uint16(h(p[2]))
	rest := p[3] + p[4]
	for i := 0; i < 8; i++ {
		g.d4[i] = byte(h(rest[i*2 : i*2+2]))
	}
	return g
}

var (
	clsidFileOpen = mkguid("DC1C5A9C-E88A-4dde-A5A1-60F82A20AEF7")
	iidFileOpen   = mkguid("d57c7288-d4ad-4768-be02-9d969532d960")
	clsidFileSave = mkguid("C0B4E2F3-BA21-4773-8DBA-335EC946EB8B")
	iidFileSave   = mkguid("84bccd23-5fde-4cdb-aea4-af64b83d78ab")
	iidShellItem  = mkguid("43826d1e-e718-42ee-bc55-a1e261c37bfe")
)

func vcall(obj uintptr, idx int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(unsafe.Pointer(obj))
	fn := *(*uintptr)(unsafe.Pointer(vtbl + uintptr(idx)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{obj}, args...)...)
	return r
}

const (
	fosPickFolders     = 0x20
	fosForceFS         = 0x40
	fosOverwritePrompt = 0x2
	errCancelled       = 0x800704C7
)

type filterSpec struct{ name, spec *uint16 }

type dlgFilter struct{ name, spec string }

func runDialog(save bool, title, start, fileName string) (string, error) {
	return runDialogF(save, false, title, start, fileName, dlgFilter{})
}

func runDialogF(save, file bool, title, start, fileName string, flt dlgFilter) (string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	clsid, iid := &clsidFileOpen, &iidFileOpen
	if save {
		clsid, iid = &clsidFileSave, &iidFileSave
	}
	var dlg uintptr
	if hr, _, _ := pCoCreateInstance.Call(uintptr(unsafe.Pointer(clsid)), 0, 1 /*CLSCTX_INPROC_SERVER*/, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&dlg))); hr != 0 {
		return "", errors.New("Couldn't open the file picker.")
	}
	defer vcall(dlg, 2)
	var opts uint32
	vcall(dlg, 10, uintptr(unsafe.Pointer(&opts)))
	if save {
		opts |= fosForceFS | fosOverwritePrompt
		name, _ := syscall.UTF16PtrFromString("CSV spreadsheet")
		spec, _ := syscall.UTF16PtrFromString("*.csv")
		f := []filterSpec{{name, spec}}
		vcall(dlg, 4, 1, uintptr(unsafe.Pointer(&f[0])))
		ext, _ := syscall.UTF16PtrFromString("csv")
		vcall(dlg, 22, uintptr(unsafe.Pointer(ext)))
		fn, _ := syscall.UTF16PtrFromString(fileName)
		vcall(dlg, 15, uintptr(unsafe.Pointer(fn)))
	} else if file {
		opts |= fosForceFS
		if flt.spec != "" {
			name, _ := syscall.UTF16PtrFromString(flt.name)
			spec, _ := syscall.UTF16PtrFromString(flt.spec)
			all, _ := syscall.UTF16PtrFromString("All files")
			star, _ := syscall.UTF16PtrFromString("*.*")
			f := []filterSpec{{name, spec}, {all, star}}
			vcall(dlg, 4, 2, uintptr(unsafe.Pointer(&f[0])))
		}
	} else {
		opts |= fosPickFolders | fosForceFS
	}
	vcall(dlg, 9, uintptr(opts))
	if title != "" {
		t, _ := syscall.UTF16PtrFromString(title)
		vcall(dlg, 17, uintptr(unsafe.Pointer(t)))
	}
	if start != "" {
		for start != "" {
			if st, err := os.Stat(start); err == nil && st.IsDir() {
				break
			}
			parent := filepath.Dir(start)
			if parent == start {
				start = ""
				break
			}
			start = parent
		}
		if start != "" {
			sp, _ := syscall.UTF16PtrFromString(start)
			var item uintptr
			if hr, _, _ := pSHCreateItemFromParsed.Call(uintptr(unsafe.Pointer(sp)), 0, uintptr(unsafe.Pointer(&iidShellItem)), uintptr(unsafe.Pointer(&item))); hr == 0 && item != 0 {
				vcall(dlg, 12, item)
				vcall(item, 2)
			}
		}
	}
	var owner uintptr
	if mainView != nil {
		owner = uintptr(mainView.Window())
	}
	hr := vcall(dlg, 3, owner)
	if uint32(hr) == errCancelled {
		return "", nil
	}
	if hr != 0 {
		return "", errors.New("The file picker didn't open.")
	}
	var item uintptr
	if vcall(dlg, 20, uintptr(unsafe.Pointer(&item))) != 0 || item == 0 {
		return "", nil
	}
	defer vcall(item, 2)
	var name *uint16
	if vcall(item, 5, 0x80058000 /*SIGDN_FILESYSPATH*/, uintptr(unsafe.Pointer(&name))) != 0 || name == nil {
		return "", errors.New("That location isn't a normal folder on disk.")
	}
	defer pCoTaskMemFree.Call(uintptr(unsafe.Pointer(name)))
	return syscall.UTF16ToString(unsafe.Slice(name, 32768)), nil
}

// dialogs must run on the window's UI thread so they're modal to it
func onUI(fn func() (string, error)) (string, error) {
	if mainView == nil {
		return fn()
	}
	type res struct {
		s   string
		err error
	}
	ch := make(chan res, 1)
	mainView.Dispatch(func() {
		s, err := fn()
		ch <- res{s, err}
	})
	r := <-ch
	return r.s, r.err
}

func pickFolder(title, start string) (string, error) {
	return onUI(func() (string, error) { return runDialog(false, title, start, "") })
}

func pickFile(title, filterName, filterSpec string) (string, error) {
	start := filepath.Join(os.Getenv("USERPROFILE"), "Downloads")
	return onUI(func() (string, error) {
		return runDialogF(false, true, title, start, "", dlgFilter{filterName, filterSpec})
	})
}

func pickSave(title, name string) (string, error) {
	return onUI(func() (string, error) { return runDialog(true, title, "", name) })
}

// ---------------- network link ----------------

var (
	iphlpapi            = syscall.NewLazyDLL("iphlpapi.dll")
	pGetBestInterfaceEx = iphlpapi.NewProc("GetBestInterfaceEx")
)

// linkInfo describes the network adapter Windows uses to reach ip.
func linkInfo(ip string) (LinkInfo, error) {
	var li LinkInfo
	parsed := net.ParseIP(strings.TrimSpace(ip)).To4()
	if parsed == nil {
		return li, errors.New("that isn't an IPv4 address")
	}
	var sa [16]byte // sockaddr_in
	sa[0] = 2       // AF_INET
	copy(sa[4:8], parsed)
	var idx uint32
	if r, _, _ := pGetBestInterfaceEx.Call(uintptr(unsafe.Pointer(&sa[0])), uintptr(unsafe.Pointer(&idx))); r != 0 {
		return li, fmt.Errorf("Windows has no route to %s", ip)
	}
	size := uint32(32 << 10)
	var buf []byte
	for i := 0; i < 3; i++ {
		buf = make([]byte, size)
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, 0, 0, (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])), &size)
		if err == nil {
			break
		}
		if err != windows.ERROR_BUFFER_OVERFLOW {
			return li, err
		}
	}
	for a := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); a != nil; a = a.Next {
		if a.IfIndex != idx && a.Ipv6IfIndex != idx {
			continue
		}
		li.Name = windows.UTF16PtrToString(a.FriendlyName)
		li.Description = windows.UTF16PtrToString(a.Description)
		li.Mbps = int64(a.TransmitLinkSpeed / 1000000)
		switch a.IfType {
		case 71:
			li.Kind = "wifi"
		case 6:
			li.Kind = "ethernet"
		default:
			li.Kind = "other"
		}
		return li, nil
	}
	return li, errors.New("couldn't find the network adapter")
}

// ---------------- recycle bin ----------------

type shFileOp struct {
	hwnd    uintptr
	wFunc   uint32
	pFrom   *uint16
	pTo     *uint16
	fFlags  uint16
	aborted int32
	mapping uintptr
	title   *uint16
}

// recycle moves path to the Recycle Bin. If Windows can't recycle it
// (too big, network drive) Windows itself asks before deleting for good.
// kept is true when the person said no.
// recycle moves a folder to the Recycle Bin. When it is too big for the bin,
// Windows asks whether to delete it for good, which stops everything until
// someone answers; with noAsk the folder skips the bin and is deleted for good
// without asking.
func recycle(path string, noAsk bool) (kept bool, err error) {
	flags := uint16(0x40 | 0x10 | 0x4 | 0x400 | 0x4000) // ALLOWUNDO|NOCONFIRMATION|SILENT|NOERRORUI|WANTNUKEWARNING
	if noAsk {
		flags &^= 0x4000 | 0x40 // no warning, and no Recycle Bin: the folder is deleted for good
	}
	from := append(syscall.StringToUTF16(filepath.Clean(path)), 0) // double-NUL terminated
	_, err = onUI(func() (string, error) {
		op := shFileOp{wFunc: 3 /*FO_DELETE*/, pFrom: &from[0],
			fFlags: flags}
		if mainView != nil {
			op.hwnd = uintptr(mainView.Window())
		}
		r, _, _ := pSHFileOperationW.Call(uintptr(unsafe.Pointer(&op)))
		if op.aborted != 0 {
			kept = true
			return "", nil
		}
		if r != 0 {
			return "", fmt.Errorf("Windows couldn't move it to the Recycle Bin (code %d). Something in the folder may be open", r)
		}
		return "", nil
	})
	if err == nil && !kept {
		if _, statErr := os.Stat(path); statErr == nil {
			return true, errors.New("the folder is still there; something in it may be open")
		}
	}
	return kept, err
}

// ---------------- window ----------------

func main() {
	if alreadyRunning() {
		return // the other copy (maybe hidden in the tray) was brought to the front
	}
	a := newApp()
	if os.Getenv("TT_TESTDIALOG") != "" { // developer check of the folder picker
		p, err := pickFolder("Test picker", `C:\`)
		msg := "picked: " + p
		if err != nil {
			msg = "error: " + err.Error()
		}
		messageBox(msg, "Loadout", 0)
	}
	url, err := a.serve()
	if err != nil {
		messageBox("Loadout couldn't start:\n"+err.Error(), "Loadout", 0x10)
		return
	}
	if a.cfg.Folder != "" {
		a.startScan("")
	}
	os.Setenv("WEBVIEW2_DEFAULT_BACKGROUND_COLOR", "FF0E1420")
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		DataPath:  filepath.Join(appDir(), "WebView2"),
		WindowOptions: webview2.WindowOptions{
			Title: "Loadout " + appVersion, Width: 1360, Height: 860, IconId: 7, Center: true,
		},
	})
	if w == nil {
		// WebView2 runtime missing: run in the default browser instead
		u, _ := syscall.UTF16PtrFromString(url)
		op, _ := syscall.UTF16PtrFromString("open")
		pShellExecuteW.Call(0, uintptr(unsafe.Pointer(op)), uintptr(unsafe.Pointer(u)), 0, 0, 1)
		messageBox("Loadout opened in your web browser because the Microsoft Edge WebView2 Runtime isn't installed.\n\n"+
			"It's free from Microsoft (search \"WebView2 Runtime\"). Once it's installed, Loadout opens in its own window.\n\n"+
			"Keep this box open while you use Loadout. Click OK to quit.", "Loadout", 0x40)
		return
	}
	mainView = w
	defer w.Destroy()
	hwnd := uintptr(w.Window())
	dark := int32(1)
	pDwmSetWindowAttribute.Call(hwnd, 20 /*DWMWA_USE_IMMERSIVE_DARK_MODE*/, uintptr(unsafe.Pointer(&dark)), 4)
	caption := uint32(0x0020140E) // COLORREF is 0x00BBGGRR, this is #0E1420
	pDwmSetWindowAttribute.Call(hwnd, 35 /*DWMWA_CAPTION_COLOR*/, uintptr(unsafe.Pointer(&caption)), 4)
	w.SetSize(1100, 680, webview2.HintMin)
	installTray(hwnd)
	w.Navigate(url)
	w.Run()
	a.shutdown()
}

// listVolumes reports the PC's drives with their file system and space.
func listVolumes() []volInfo {
	var res []volInfo
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil
	}
	sysDrive := strings.ToUpper(filepath.VolumeName(os.Getenv("SystemRoot")))
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		rp, _ := windows.UTF16PtrFromString(root)
		t := windows.GetDriveType(rp)
		if t != windows.DRIVE_FIXED && t != windows.DRIVE_REMOVABLE {
			continue // CD drives, network shares, RAM disks
		}
		label := make([]uint16, 261)
		fs := make([]uint16, 261)
		if windows.GetVolumeInformation(rp, &label[0], uint32(len(label)), nil, nil, nil, &fs[0], uint32(len(fs))) != nil {
			continue // empty card reader slot and the like
		}
		v := volInfo{Root: root, Label: windows.UTF16ToString(label), FS: windows.UTF16ToString(fs), Total: -1, Free: -1,
			Removable: t == windows.DRIVE_REMOVABLE, System: strings.EqualFold(root[:2], sysDrive)}
		var free, total, totalFree uint64
		if windows.GetDiskFreeSpaceEx(rp, &free, &total, &totalFree) == nil {
			v.Free, v.Total = int64(free), int64(total)
		}
		res = append(res, v)
	}
	return res
}

// hideChild keeps a helper program from flashing a window.
func hideChild(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
