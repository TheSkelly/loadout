package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Backporting a program: the steps BackPork takes, done here.
//   1. a fake-signed program (fSELF) is turned back into the ELF inside it
//   2. the SDK in its process or module parameters is lowered to the
//      console's firmware
//   3. it's signed again as an fSELF (make_fself.py, from the PS5 payload
//      SDK, run as its own program)
// The fakelib folder of newer system libraries the game needs is kept if
// the game has one, or copied in from a folder you choose.

// fselfToELF rebuilds the ELF a fake-signed SELF carries. Each segment's
// bytes sit in the SELF at the offset its entry gives; the version segment
// is the SELF's last bytes.
func fselfToELF(self []byte) ([]byte, error) {
	le := binary.LittleEndian
	if len(self) < 0x20 || (string(self[:4]) != string(ps5SelfMag) && string(self[:4]) != string(ps4SelfMag)) {
		return nil, errors.New("not a SELF")
	}
	n := int(le.Uint16(self[0x18:]))
	elfAt := 0x20 + 0x20*n
	if n <= 0 || elfAt+0x40 > len(self) || string(self[elfAt:elfAt+4]) != string(elfMagic) {
		return nil, errors.New("no ELF header in the SELF")
	}
	eh := self[elfAt:]
	phoff, phnum := int(le.Uint64(eh[0x20:])), int(le.Uint16(eh[0x38:]))
	if phnum <= 0 || phnum > 64 || elfAt+phoff+phnum*0x38 > len(self) {
		return nil, errors.New("odd program header table")
	}
	ph := eh[phoff : phoff+phnum*0x38]
	type ent struct {
		off, size      int
		enc, compr, ok bool
	}
	segs := make([]ent, phnum)
	for i := 0; i < n; i++ {
		e := self[0x20+i*0x20:]
		props := le.Uint64(e)
		if props&0x800 == 0 {
			continue
		}
		idx := int(props>>20) & 0xFFF
		if idx < phnum {
			segs[idx] = ent{off: int(le.Uint64(e[8:])), size: int(le.Uint64(e[16:])), enc: props&2 != 0, compr: props&8 != 0, ok: true}
		}
	}
	// the ELF ends where its last segment does
	end := phoff + phnum*0x38
	for i := 0; i < phnum; i++ {
		p := ph[i*0x38:]
		if e := int(le.Uint64(p[8:]) + le.Uint64(p[0x20:])); e > end {
			end = e
		}
	}
	if end > 1<<31 {
		return nil, errors.New("too big")
	}
	out := make([]byte, end)
	copy(out, eh[:phoff+phnum*0x38]) // header and program headers
	for i := 0; i < phnum; i++ {
		p := ph[i*0x38:]
		typ := le.Uint32(p)
		off, size := int(le.Uint64(p[8:])), int(le.Uint64(p[0x20:]))
		if size == 0 {
			continue
		}
		if typ == 0x6FFFFF01 { // PT_SCE_VERSION: kept at the very end of the SELF
			if size > len(self) {
				return nil, errors.New("version segment past the end")
			}
			copy(out[off:], self[len(self)-size:])
			continue
		}
		s := segs[i]
		if !s.ok {
			continue // lies inside another segment, or has no bytes of its own
		}
		if s.enc || s.compr {
			return nil, fmt.Errorf("segment %d is encrypted or compressed", i)
		}
		if s.off+size > len(self) {
			return nil, fmt.Errorf("segment %d runs past the end of the SELF", i)
		}
		copy(out[off:off+size], self[s.off:s.off+size])
	}
	return out, nil
}

// patchELFSDK lowers the PS5 (and PS4) SDK in an ELF's parameter segment.
// It answers the SDK it had.
func patchELFSDK(elf []byte, ps5, ps4 uint32) (uint32, error) {
	le := binary.LittleEndian
	if len(elf) < 0x40 || string(elf[:4]) != string(elfMagic) {
		return 0, errors.New("not an ELF")
	}
	phoff, phnum := int(le.Uint64(elf[0x20:])), int(le.Uint16(elf[0x38:]))
	for i := 0; i < phnum; i++ {
		p := elf[phoff+i*0x38:]
		t := le.Uint32(p)
		if t != ptSceProcParam && t != ptSceModuleParam {
			continue
		}
		off := int(le.Uint64(p[8:]))
		if off+0x18 > len(elf) {
			return 0, errors.New("parameters past the end")
		}
		magic := le.Uint32(elf[off+8:])
		if (t == ptSceProcParam && magic != sceProcMagic) || (t == ptSceModuleParam && magic != sceModuleMagic) {
			return 0, errors.New("parameters aren't readable")
		}
		old := le.Uint32(elf[off+0x14:])
		if old > ps5 {
			le.PutUint32(elf[off+0x14:], ps5)
			if le.Uint32(elf[off+0x10:]) > ps4 {
				le.PutUint32(elf[off+0x10:], ps4)
			}
		}
		return old, nil
	}
	return 0, errors.New("no SDK parameters in it")
}

// backportTargets is what a program is lowered to for a console firmware:
// the PS5 SDK, and the PS4 SDK that goes with it (from BackPork Kitchen's
// table, which pairs each PS5 firmware with a PS4 SDK half a version on).
func backportTargets(fw string) (ps5, ps4 uint32, err error) {
	n := fwValue(fw)
	if n < 100 {
		return 0, 0, errors.New("set your PS5's firmware in Settings first")
	}
	major := n / 100
	fmt.Sscanf(fmt.Sprintf("%02d%02d", major, n%100), "%x", &ps5)
	ps5 = ps5<<16 | 0x0040
	// PS4 SDK: 1 -> 7.59, 2 -> 8.05, ... 6 -> 10.09, then +0.50 per firmware
	table := map[int]uint32{1: 0x07590001, 2: 0x08050001, 3: 0x08540001, 4: 0x09040001, 5: 0x09590001}
	if v, ok := table[major]; ok {
		return ps5, v, nil
	}
	half := 1009 + (major-6)*50 // 10.09, 10.59, 11.09 ...
	if half%100 >= 60 {
		half += 40 // 10.59 + 0.50 = 11.09
	}
	fmt.Sscanf(fmt.Sprintf("%02d%02d", half/100, half%100), "%x", &ps4)
	return ps5, ps4<<16 | 0x0001, nil
}

// paramRequiredRe finds the firmware a game's param.json says it needs.
var paramRequiredRe = regexp.MustCompile(`("requiredSystemSoftwareVersion"\s*:\s*")0x([0-9A-Fa-f]{16})(")`)

// paramFirmware is what a param.json asks for, as "13.60" ("" if it doesn't say).
func paramFirmware(param []byte) string {
	m := paramRequiredRe.FindSubmatch(param)
	if m == nil {
		return ""
	}
	v := string(m[2])
	return strings.TrimLeft(v[0:2], "0") + "." + v[2:4]
}

// lowerParamFirmware sets the firmware a param.json asks for to the console's
// when it asks for newer: the PS5 won't start a game that says it needs a
// newer firmware, even one built for this one. The rest of the file is kept
// exactly as it was.
func lowerParamFirmware(param []byte, fw string) ([]byte, string, bool) {
	have := paramFirmware(param)
	if have == "" || fwValue(have) <= fwValue(fw) {
		return param, have, false
	}
	n := fwValue(fw)
	val := fmt.Sprintf("%02d%02d000000000000", n/100, n%100)
	out := paramRequiredRe.ReplaceAll(param, []byte("${1}0x"+val+"${3}"))
	return out, have, true
}
