package main

// verify.go: reads an exFAT image back the way a driver would (boot
// sector, FAT chains, directory entry sets) and compares it, byte for
// byte, with the folder it was built from. Used before a source folder
// is ever removed.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

// names that may exist in an image made by other tools (Windows adds
// these when a volume is mounted) and never in a game dump we care about
var verifySkip = map[string]bool{"system volume information": true, "$recycle.bin": true}

type exImage struct {
	f          *os.File
	heap       int64 // byte offset of cluster 2
	cs         int64 // cluster size
	fat        []byte
	clusters   uint32
	totalRead  int64
	total      int64
	progress   ExfatProgress
	cancelled  func() bool
	bufA, bufB []byte
	sums       map[string]FileSum // from the build: lets the check read only the image
}

type exEntry struct {
	name    string
	isDir   bool
	first   uint32
	length  uint64
	noChain bool
}

func (im *exImage) next(c uint32) uint32 {
	if int(c)*4+4 > len(im.fat) {
		return exEOC
	}
	return binary.LittleEndian.Uint32(im.fat[c*4:])
}

// runs returns the (first cluster, count) runs holding length bytes.
func (im *exImage) runs(first uint32, length uint64, noChain bool) ([][2]uint32, error) {
	need := uint32((int64(length) + im.cs - 1) / im.cs)
	if need == 0 {
		return nil, nil
	}
	if first < 2 || first-2 >= im.clusters {
		return nil, errors.New("bad first cluster")
	}
	if noChain {
		if first-2+need > im.clusters {
			return nil, errors.New("file runs past the end of the volume")
		}
		return [][2]uint32{{first, need}}, nil
	}
	var out [][2]uint32
	c, got := first, uint32(0)
	for got < need {
		if c < 2 || c-2 >= im.clusters {
			return nil, errors.New("broken cluster chain")
		}
		if n := len(out); n > 0 && out[n-1][0]+out[n-1][1] == c {
			out[n-1][1]++
		} else {
			out = append(out, [2]uint32{c, 1})
		}
		got++
		if got < need {
			c = im.next(c)
		}
	}
	return out, nil
}

func (im *exImage) readAll(first uint32, length uint64, noChain bool) ([]byte, error) {
	rs, err := im.runs(first, length, noChain)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 0, length)
	for _, r := range rs {
		b := make([]byte, int64(r[1])*im.cs)
		if _, err := im.f.ReadAt(b, im.heap+int64(r[0]-2)*im.cs); err != nil {
			return nil, err
		}
		buf = append(buf, b...)
	}
	if uint64(len(buf)) > length {
		buf = buf[:length]
	}
	return buf, nil
}

func (im *exImage) listDir(first uint32, length uint64, noChain bool) ([]exEntry, error) {
	data, err := im.readAll(first, length, noChain)
	if err != nil {
		return nil, err
	}
	var out []exEntry
	for i := 0; i+32 <= len(data); {
		t := data[i]
		if t == 0 {
			break
		}
		if t != 0x85 {
			i += 32
			continue
		}
		n := int(data[i+1])
		if n < 2 || i+32*(n+1) > len(data) {
			return nil, errors.New("damaged directory entry")
		}
		set := data[i : i+32*(n+1)]
		if setChecksum(set) != binary.LittleEndian.Uint16(set[2:]) {
			return nil, errors.New("directory entry checksum is wrong")
		}
		attr := binary.LittleEndian.Uint16(set[4:])
		st := set[32:64]
		nameLen := int(st[3])
		var u []uint16
		for k := 2; k <= n; k++ {
			e := set[k*32 : k*32+32]
			for j := 0; j < 15; j++ {
				u = append(u, binary.LittleEndian.Uint16(e[2+j*2:]))
			}
		}
		if nameLen > len(u) {
			return nil, errors.New("damaged file name")
		}
		out = append(out, exEntry{
			name:    string(utf16.Decode(u[:nameLen])),
			isDir:   attr&exAttrDir != 0,
			first:   binary.LittleEndian.Uint32(st[20:]),
			length:  binary.LittleEndian.Uint64(st[24:]),
			noChain: st[1]&0x02 != 0,
		})
		i += 32 * (n + 1)
	}
	return out, nil
}

func (im *exImage) compareFile(e exEntry, path string) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	fi, err := src.Stat()
	if err != nil {
		return err
	}
	if uint64(fi.Size()) != e.length {
		return fmt.Errorf("%s is a different size in the image", e.name)
	}
	rs, err := im.runs(e.first, e.length, e.noChain)
	if err != nil {
		return fmt.Errorf("%s: %v", e.name, err)
	}
	// When the build recorded this file's checksum and the file hasn't changed
	// since, reading the image alone is enough; the source isn't read again.
	ks, known := im.sums[path]
	known = known && ks.Size == fi.Size() && ks.MTime.Equal(fi.ModTime())
	crc := crc32.New(castagnoli)
	// the buffers are shared across files: games have thousands of small ones
	if im.bufA == nil {
		im.bufA = make([]byte, 8<<20)
	}
	a := im.bufA
	var b []byte
	if !known {
		if im.bufB == nil {
			im.bufB = make([]byte, 8<<20)
		}
		b = im.bufB
	}
	left := int64(e.length)
	for _, r := range rs {
		off := im.heap + int64(r[0]-2)*im.cs
		span := int64(r[1]) * im.cs
		for span > 0 && left > 0 {
			if im.cancelled != nil && im.cancelled() {
				return ErrCancelled
			}
			k := int64(len(a))
			if k > span {
				k = span
			}
			if k > left {
				k = left
			}
			if _, err := im.f.ReadAt(a[:k], off); err != nil {
				return err
			}
			if known {
				crc.Write(a[:k])
			} else {
				if _, err := io.ReadFull(src, b[:k]); err != nil {
					return fmt.Errorf("couldn't read %s: %v", e.name, err)
				}
				if !bytes.Equal(a[:k], b[:k]) {
					return fmt.Errorf("%s doesn't match the image", e.name)
				}
			}
			off += k
			span -= k
			left -= k
			im.totalRead += k
			if im.progress != nil {
				im.progress(im.totalRead, im.total, path)
			}
		}
	}
	if known && crc.Sum32() != ks.Sum {
		return fmt.Errorf("%s doesn't match the image", e.name)
	}
	return nil
}

func (im *exImage) compareDir(entries []exEntry, dir string) error {
	have := map[string]exEntry{}
	for _, e := range entries {
		if verifySkip[strings.ToLower(e.name)] {
			continue
		}
		have[strings.ToLower(e.name)] = e
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	seen := 0
	for _, d := range ents {
		lname := strings.ToLower(d.Name())
		if exIgnored[lname] || verifySkip[lname] {
			continue
		}
		e, ok := have[lname]
		if !ok {
			return fmt.Errorf("%s is missing from the image", filepath.Join(filepath.Base(dir), d.Name()))
		}
		seen++
		p := filepath.Join(dir, d.Name())
		if d.IsDir() != e.isDir {
			return fmt.Errorf("%s is a different type in the image", d.Name())
		}
		if e.isDir {
			sub, err := im.listDir(e.first, e.length, e.noChain)
			if err != nil {
				return fmt.Errorf("%s: %v", d.Name(), err)
			}
			if err := im.compareDir(sub, p); err != nil {
				return err
			}
			continue
		}
		if err := im.compareFile(e, p); err != nil {
			return err
		}
	}
	if seen != len(have) {
		return errors.New("the image has files that aren't in the folder")
	}
	return nil
}

// VerifyExfat confirms that image holds exactly the contents of src.
func VerifyExfat(image, src string, progress ExfatProgress, cancelled func() bool) error {
	return VerifyExfatSums(image, src, nil, progress, cancelled)
}

// VerifyExfatSums is VerifyExfat with the checksums recorded while building,
// which lets it skip reading the source files a second time.
func VerifyExfatSums(image, src string, sums map[string]FileSum, progress ExfatProgress, cancelled func() bool) error {
	f, err := os.Open(image)
	if err != nil {
		return err
	}
	defer f.Close()
	boot := make([]byte, 512)
	if _, err := f.ReadAt(boot, 0); err != nil {
		return errors.New("the image file is empty or damaged")
	}
	if string(boot[3:11]) != "EXFAT   " || boot[510] != 0x55 || boot[511] != 0xAA {
		return errors.New("that file isn't an exFAT image")
	}
	le := binary.LittleEndian
	bps := int64(1) << boot[108]
	im := &exImage{f: f, cs: bps << boot[109], progress: progress, cancelled: cancelled, sums: sums}
	fatOff := int64(le.Uint32(boot[80:])) * bps
	fatLen := int64(le.Uint32(boot[84:])) * bps
	im.heap = int64(le.Uint32(boot[88:])) * bps
	im.clusters = le.Uint32(boot[92:])
	root := le.Uint32(boot[96:])
	if fatLen <= 0 || fatLen > 1<<30 {
		return errors.New("the image's FAT looks damaged")
	}
	im.fat = make([]byte, fatLen)
	if _, err := f.ReadAt(im.fat, fatOff); err != nil {
		return errors.New("couldn't read the image's FAT")
	}
	if fi, err := f.Stat(); err != nil || fi.Size() < im.heap+int64(im.clusters)*im.cs {
		return errors.New("the image file is shorter than its volume, it may be incomplete")
	}
	// root directory length: follow its chain
	n, c := uint32(0), root
	for c >= 2 && c-2 < im.clusters && n <= im.clusters {
		n++
		c = im.next(c)
	}
	rootEntries, err := im.listDir(root, uint64(n)*uint64(im.cs), false)
	if err != nil {
		return fmt.Errorf("root folder: %v", err)
	}
	im.total = folderSize(src)
	return im.compareDir(rootEntries, src)
}

// removableRoot checks that everything inside folder is the game root
// (directly, or through single nested folders like Name/PPSA01234-app),
// so removing folder can't lose anything that isn't in the image.
func removableRoot(folder, root string) error {
	folder, root = filepath.Clean(folder), filepath.Clean(root)
	if root == "" || root == "." {
		return errors.New("no game files found")
	}
	cur := root
	for cur != folder {
		parent := filepath.Dir(cur)
		if parent == cur || len(parent) < len(folder) {
			return errors.New("the game isn't inside this folder")
		}
		ents, err := os.ReadDir(parent)
		if err != nil {
			return err
		}
		for _, e := range ents {
			if strings.EqualFold(e.Name(), filepath.Base(cur)) || exIgnored[strings.ToLower(e.Name())] {
				continue
			}
			return fmt.Errorf("the folder also holds \"%s\", which isn't in the image", e.Name())
		}
		cur = parent
	}
	return nil
}
