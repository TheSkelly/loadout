package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Checking a backport for real. A fakelib folder alone doesn't make a game
// run on older firmware: each of its programs (eboot.bin and the .prx/.sprx
// modules) records the SDK it was built with in its process or module
// parameters, and BackPork lowers that number to the console's firmware.
// This reads the number from every program, in a game folder or inside an
// exFAT image, so a "backported" game whose programs still ask for newer
// firmware is caught.
//
// Programs are plain ELF or fake-signed SELF (fSELF) in game dumps; a
// retail-encrypted SELF can't be read and is reported as such.

const (
	ptSceProcParam   = 0x61000001
	ptSceModuleParam = 0x61000002
	sceProcMagic     = 0x4942524F // "ORBI"
	sceModuleMagic   = 0x3C13F4BF
)

var (
	elfMagic   = []byte{0x7F, 'E', 'L', 'F'}
	ps5SelfMag = []byte{0x54, 0x14, 0xF5, 0xEE}
	ps4SelfMag = []byte{0x4F, 0x15, 0x3D, 0x1D}
)

// progSDK is one program and the SDK it asks for.
type progSDK struct {
	Name string `json:"name"`
	SDK  string `json:"sdk"`  // "13.00", "" when it couldn't be read
	Kind string `json:"kind"` // elf, fself, encrypted, unreadable
	raw  uint32
}

// backportCheck is what a game's programs say.
type backportCheck struct {
	Programs     []progSDK `json:"programs"`
	Highest      string    `json:"highest"`    // the newest SDK any program asks for
	Fakelib      int       `json:"fakelib"`    // files in the fakelib folder
	Unreadable   int       `json:"unreadable"` // programs whose SDK couldn't be read
	Verdict      string    `json:"verdict"`    // runs, needs, incomplete, unknown
	Explanation  string    `json:"explanation"`
	highestValue uint32
}

// sdkText: 0x13000038 -> "13.00" (the version is stored as hex digits).
func sdkText(v uint32) string {
	return fmt.Sprintf("%x.%02x", v>>24, (v>>16)&0xFF)
}

// sdkOfFirmware: "12.00" -> 0x1200FFFF, the highest SDK that firmware runs.
func sdkOfFirmware(fw string) uint32 {
	n := fwValue(fw)
	if n < 0 {
		return 0
	}
	var v uint32
	fmt.Sscanf(fmt.Sprintf("%02d%02d", n/100, n%100), "%x", &v)
	return v<<16 | 0xFFFF
}

// programSDK reads the PS5 SDK from a program's process or module parameters.
func programSDK(r io.ReaderAt, size int64) (uint32, string, error) {
	head := make([]byte, 0x40)
	if _, err := r.ReadAt(head, 0); err != nil {
		return 0, "unreadable", err
	}
	switch {
	case string(head[:4]) == string(elfMagic):
		v, err := elfParamSDK(r, 0, func(off int64) int64 { return off })
		return v, "elf", err
	case string(head[:4]) == string(ps5SelfMag) || string(head[:4]) == string(ps4SelfMag):
		return selfParamSDK(r)
	}
	return 0, "unreadable", errors.New("not a program")
}

// elfParamSDK finds the parameter segment in an ELF at base; at maps a
// segment's file offset to where its bytes are.
func elfParamSDK(r io.ReaderAt, base int64, at func(int64) int64) (uint32, error) {
	h := make([]byte, 0x40)
	if _, err := r.ReadAt(h, base); err != nil {
		return 0, err
	}
	le := binary.LittleEndian
	phoff, phnum := int64(le.Uint64(h[0x20:])), int(le.Uint16(h[0x38:]))
	if phnum <= 0 || phnum > 64 {
		return 0, errors.New("odd program header table")
	}
	ph := make([]byte, 0x38*phnum)
	if _, err := r.ReadAt(ph, base+phoff); err != nil {
		return 0, err
	}
	for i := 0; i < phnum; i++ {
		p := ph[i*0x38:]
		t := le.Uint32(p)
		if t != ptSceProcParam && t != ptSceModuleParam {
			continue
		}
		off := at(int64(le.Uint64(p[8:])))
		if off < 0 {
			return 0, errors.New("parameters are in an encrypted or compressed part")
		}
		prm := make([]byte, 0x18)
		if _, err := r.ReadAt(prm, off); err != nil {
			return 0, err
		}
		magic := le.Uint32(prm[8:])
		if (t == ptSceProcParam && magic != sceProcMagic) || (t == ptSceModuleParam && magic != sceModuleMagic) {
			return 0, errors.New("parameters aren't readable (encrypted?)")
		}
		return le.Uint32(prm[0x14:]), nil
	}
	return 0, errors.New("no SDK parameters in it")
}

// selfParamSDK reads a SELF: its segment table, then the ELF header after it.
func selfParamSDK(r io.ReaderAt) (uint32, string, error) {
	h := make([]byte, 0x20)
	if _, err := r.ReadAt(h, 0); err != nil {
		return 0, "unreadable", err
	}
	le := binary.LittleEndian
	n := int(le.Uint16(h[0x18:]))
	if n <= 0 || n > 256 {
		return 0, "unreadable", errors.New("odd SELF header")
	}
	ents := make([]byte, 0x20*n)
	if _, err := r.ReadAt(ents, 0x20); err != nil {
		return 0, "unreadable", err
	}
	type seg struct {
		off        int64
		enc, compr bool
	}
	segs := map[int]seg{}
	encrypted := false
	for i := 0; i < n; i++ {
		e := ents[i*0x20:]
		props := le.Uint64(e)
		if props&0x800 == 0 { // only the entries that hold a segment's data
			continue
		}
		s := seg{off: int64(le.Uint64(e[8:])), enc: props&0x2 != 0, compr: props&0x8 != 0}
		segs[int(props>>20)&0xFFF] = s
		if s.enc {
			encrypted = true
		}
	}
	elfAt := int64(0x20 + 0x20*n)
	// the segment a file offset falls in decides where its bytes are in the SELF
	hd := make([]byte, 0x40)
	if _, err := r.ReadAt(hd, elfAt); err != nil || string(hd[:4]) != string(elfMagic) {
		return 0, "unreadable", errors.New("no ELF header in the SELF")
	}
	phoff, phnum := int64(le.Uint64(hd[0x20:])), int(le.Uint16(hd[0x38:]))
	ph := make([]byte, 0x38*phnum)
	if _, err := r.ReadAt(ph, elfAt+phoff); err != nil {
		return 0, "unreadable", err
	}
	at := func(off int64) int64 {
		for i := 0; i < phnum; i++ {
			p := ph[i*0x38:]
			start, size := int64(le.Uint64(p[8:])), int64(le.Uint64(p[0x20:]))
			if off >= start && off < start+size {
				s, ok := segs[i]
				if !ok || s.enc || s.compr {
					return -1
				}
				return s.off + (off - start)
			}
		}
		return -1
	}
	// the header parse above already read the table; elfParamSDK reads it again at elfAt
	v, err := elfParamSDK(r, elfAt, at)
	if err != nil && encrypted {
		return 0, "encrypted", errors.New("the program is encrypted")
	}
	return v, "fself", err
}

// skipDir: folders backport tools leave behind (the untouched originals),
// which aren't what the console runs.
func skipDir(name string) bool {
	n := strings.ToLower(name)
	return n == "fakelib" || n == "decrypted" || n == "sce_sys" || strings.HasPrefix(n, "backup") || strings.HasSuffix(n, "_backup") || strings.HasPrefix(n, "_")
}

// the programs that matter are at the top and in sce_module
func lookIn(rel string) bool {
	return rel == "." || strings.EqualFold(rel, "sce_module")
}

func isProgram(name string) bool {
	n := strings.ToLower(name)
	return n == "eboot.bin" || strings.HasSuffix(n, ".prx") || strings.HasSuffix(n, ".sprx") || strings.HasSuffix(n, ".elf") || strings.HasSuffix(n, ".self")
}

// checkFolderBackport reads every program in a game folder (fakelib aside).
func checkFolderBackport(root string) (*backportCheck, error) {
	c := &backportCheck{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		top := strings.ToLower(strings.Split(filepath.ToSlash(rel), "/")[0])
		if d.IsDir() {
			if strings.EqualFold(rel, "fakelib") {
				if ents, err := os.ReadDir(p); err == nil {
					c.Fakelib = len(ents)
				}
				return filepath.SkipDir
			}
			if lookIn(rel) && !(rel != "." && skipDir(d.Name())) {
				return nil
			}
			return filepath.SkipDir
		}
		_ = top
		if !isProgram(d.Name()) {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		st, _ := f.Stat()
		c.add(filepath.ToSlash(rel), f, st.Size())
		return nil
	})
	c.finish()
	return c, err
}

// checkImageBackport does the same inside an exFAT image.
func checkImageBackport(image string) (*backportCheck, error) {
	im, root, err := openExImage(image)
	if err != nil {
		return nil, err
	}
	defer im.f.Close()
	c := &backportCheck{}
	var walk func(ents []exEntry, prefix string, depth int)
	walk = func(ents []exEntry, prefix string, depth int) {
		for _, e := range ents {
			rel := prefix + e.name
			if e.isDir {
				if depth == 0 && strings.EqualFold(e.name, "fakelib") {
					if in, err := im.listDir(e.first, e.length, e.noChain); err == nil {
						c.Fakelib = len(in)
					}
					continue
				}
				if depth == 0 && strings.EqualFold(e.name, "sce_module") {
					if in, err := im.listDir(e.first, e.length, e.noChain); err == nil {
						walk(in, rel+"/", depth+1)
					}
				}
				continue
			}
			if !isProgram(e.name) {
				continue
			}
			runs, err := im.runs(e.first, e.length, e.noChain)
			if err != nil {
				c.Programs = append(c.Programs, progSDK{Name: rel, Kind: "unreadable"})
				continue
			}
			c.add(rel, &exFileReader{im: im, runs: runs, size: int64(e.length)}, int64(e.length))
		}
	}
	walk(root, "", 0)
	c.finish()
	return c, nil
}

func (c *backportCheck) add(name string, r io.ReaderAt, size int64) {
	v, kind, err := programSDK(r, size)
	p := progSDK{Name: name, Kind: kind}
	if err == nil && v != 0 {
		p.SDK, p.raw = sdkText(v), v
		if v > c.highestValue {
			c.highestValue = v
		}
	} else {
		c.Unreadable++
	}
	c.Programs = append(c.Programs, p)
}

func (c *backportCheck) finish() {
	sort.Slice(c.Programs, func(i, j int) bool { return c.Programs[i].raw > c.Programs[j].raw })
	if c.highestValue != 0 {
		c.Highest = sdkText(c.highestValue)
	}
}

// judge says whether the game runs on the console's firmware.
func (c *backportCheck) judge(consoleFW string) {
	fw := sdkOfFirmware(consoleFW)
	switch {
	case consoleFW == "" || fw == 0:
		c.Verdict, c.Explanation = "unknown", "Set your PS5's firmware in Settings so Loadout can tell."
	case c.highestValue == 0:
		c.Verdict, c.Explanation = "unknown", "Loadout couldn't read the SDK from this game's programs (they may be encrypted)."
	case c.highestValue <= fw && c.Fakelib > 0:
		c.Verdict, c.Explanation = "runs", fmt.Sprintf("Backported: every program asks for %s or older, and it has a fakelib folder.", c.Highest)
	case c.highestValue <= fw:
		c.Verdict, c.Explanation = "runs", fmt.Sprintf("Its programs ask for %s or older, so it runs on %s.", c.Highest, consoleFW)
	case c.Fakelib > 0:
		c.Verdict = "incomplete"
		c.Explanation = fmt.Sprintf("It has a fakelib folder, but %s still asks for SDK %s, newer than your %s. The backport wasn't finished, so it won't start.",
			c.Programs[0].Name, c.Highest, consoleFW)
	default:
		c.Verdict, c.Explanation = "needs", fmt.Sprintf("Built with SDK %s, newer than your %s, and not backported.", c.Highest, consoleFW)
	}
}

// exFileReader reads one file inside an exFAT image.
type exFileReader struct {
	im   *exImage
	runs [][2]uint32
	size int64
}

func (r *exFileReader) ReadAt(p []byte, off int64) (int, error) {
	if off >= r.size {
		return 0, io.EOF
	}
	if off+int64(len(p)) > r.size {
		p = p[:r.size-off]
	}
	n := 0
	pos := int64(0)
	for _, run := range r.runs {
		runLen := int64(run[1]) * r.im.cs
		if off < pos+runLen && n < len(p) {
			in := off - pos
			if in < 0 {
				in = 0
			}
			want := len(p) - n
			if int64(want) > runLen-in {
				want = int(runLen - in)
			}
			at := r.im.heap + int64(run[0]-2)*r.im.cs + in
			m, err := r.im.f.ReadAt(p[n:n+want], at)
			n += m
			off += int64(m)
			if err != nil {
				return n, err
			}
		}
		pos += runLen
		if n == len(p) {
			break
		}
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
