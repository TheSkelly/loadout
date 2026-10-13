//go:build windows

package main

import (
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	powrprof   = syscall.NewLazyDLL("powrprof.dll")
	awakeMu    sync.Mutex
	awakeState string // what the keep-awake loop is doing right now, for Settings
)

func setAwakeState(s string) { awakeMu.Lock(); awakeState = s; awakeMu.Unlock() }

// keepAwakeLoop stops Windows from sleeping while busy() is true. It holds
// power requests (the kind "powercfg /requests" lists) for the system AND the
// screen: on "Modern Standby" PCs, which is most laptops and many newer
// desktops, the screen turning off by itself is what puts the PC to sleep and
// cuts the network, so only keeping the screen on stops that. It also resets
// Windows' idle timers every few seconds in case a request is ignored.
func keepAwakeLoop(busy func() bool) {
	runtime.LockOSThread()
	const esContinuous, esSystemRequired, esDisplayRequired, esAwayMode = 0x80000000, 0x00000001, 0x00000002, 0x00000040
	const reqDisplay, reqSystem, reqAwayMode, reqExecution = 0, 1, 2, 3
	type reasonContext struct {
		Version uint32
		Flags   uint32
		Reason  *uint16
	}
	create := kernel32.NewProc("PowerCreateRequest")
	set := kernel32.NewProc("PowerSetRequest")
	clear := kernel32.NewProc("PowerClearRequest")
	var req uintptr
	if create.Find() == nil {
		why, _ := syscall.UTF16PtrFromString("Loadout is building or sending a game")
		rc := reasonContext{Version: 0, Flags: 1, Reason: why}
		h, _, _ := create.Call(uintptr(unsafe.Pointer(&rc)))
		if h != 0 && h != ^uintptr(0) {
			req = h
		}
	}
	setAwakeState("idle")
	on := false
	for {
		b := busy()
		if b != on {
			on = b
			ok := req != 0
			for _, t := range []uintptr{reqSystem, reqDisplay, reqExecution, reqAwayMode} {
				if req == 0 {
					break
				}
				if on {
					r, _, _ := set.Call(req, t)
					if r == 0 && t == reqSystem {
						ok = false
					}
				} else {
					clear.Call(req, t)
				}
			}
			if !on {
				pSetThreadExecState.Call(esContinuous)
				setAwakeState("idle")
			} else if ok {
				setAwakeState("on")
			} else {
				setAwakeState("partial")
			}
		}
		if on {
			// said again every round: this also restarts Windows' idle countdowns
			pSetThreadExecState.Call(esContinuous | esSystemRequired | esDisplayRequired | esAwayMode)
			pSetThreadExecState.Call(esSystemRequired | esDisplayRequired)
		}
		time.Sleep(5 * time.Second)
	}
}

type winGUID struct {
	D1     uint32
	D2, D3 uint16
	D4     [8]byte
}

var (
	guidButtons = winGUID{0x4f971e89, 0xeebd, 0x4455, [8]byte{0xa8, 0xde, 0x9e, 0x59, 0x04, 0x0e, 0x73, 0x47}}
	guidLid     = winGUID{0x5ca83367, 0x6e45, 0x459f, [8]byte{0xa2, 0x7b, 0x47, 0x6b, 0x1d, 0x01, 0xc9, 0x36}}
)

// lidAction is what closing the lid does: 0 nothing, 1 sleep, 2 hibernate,
// 3 shut down; -1 when Windows won't say. ac picks plugged in or battery.
func lidAction(ac bool) int {
	get := powrprof.NewProc("PowerGetActiveScheme")
	if get.Find() != nil {
		return -1
	}
	var scheme *winGUID
	if r, _, _ := get.Call(0, uintptr(unsafe.Pointer(&scheme))); r != 0 || scheme == nil {
		return -1
	}
	defer kernel32.NewProc("LocalFree").Call(uintptr(unsafe.Pointer(scheme)))
	name := "PowerReadDCValueIndex"
	if ac {
		name = "PowerReadACValueIndex"
	}
	var v uint32
	r, _, _ := powrprof.NewProc(name).Call(0, uintptr(unsafe.Pointer(scheme)), uintptr(unsafe.Pointer(&guidButtons)), uintptr(unsafe.Pointer(&guidLid)), uintptr(unsafe.Pointer(&v)))
	if r != 0 {
		return -1
	}
	return int(v)
}

// powerFacts says whether this PC has a lid and uses Modern Standby.
func powerFacts() (lid, modern bool) {
	var caps [256]byte
	p := powrprof.NewProc("GetPwrCapabilities")
	if p.Find() != nil {
		return
	}
	if r, _, _ := p.Call(uintptr(unsafe.Pointer(&caps[0]))); r == 0 {
		return
	}
	return caps[2] != 0, caps[20] != 0
}

// awakeNotes is the plain-words status shown under the Settings switch.
func awakeNotes() (state string, notes []string) {
	awakeMu.Lock()
	state = awakeState
	awakeMu.Unlock()
	lid, modern := powerFacts()
	if lid {
		if a := lidAction(true); a > 0 {
			notes = append(notes, "Closing this laptop's lid still puts it to sleep, and no app can stop that. Leave the lid open during jobs, or set \"When I close the lid\" to \"Do nothing\" in Windows' power settings.")
		} else if a := lidAction(false); a > 0 {
			notes = append(notes, "On battery, closing the lid puts this laptop to sleep. Keep it plugged in with the lid open during jobs.")
		}
	}
	if modern {
		notes = append(notes, "This PC uses Modern Standby, so the screen stays on while a job runs (when the screen goes off, this kind of PC goes to sleep and drops the network). It can turn off again as soon as the job is done.")
	}
	return
}

func awakeStateOnly() (string, bool) { awakeMu.Lock(); defer awakeMu.Unlock(); return awakeState, true }
