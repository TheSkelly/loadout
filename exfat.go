package main

// Native exFAT image writer, written from Microsoft's published exFAT
// specification. It builds a complete, read-ready exFAT volume from a
// folder in one forward pass: boot regions, FAT, then the cluster heap
// (allocation bitmap, up-case table, root directory, then every directory
// and file laid out contiguously). No drivers, mounting or admin rights.

import (
	"bufio"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

//go:embed upcase.bin
var upcaseTable []byte // standard compressed up-case table (5836 bytes)

const (
	exSector      = 512
	exFatOffset   = 2048 // sectors (1 MB), same as mkfs.exfat
	exEOC         = 0xFFFFFFFF
	exAttrDir     = 0x10
	exAttrArchive = 0x20
)

var upcaseMap = func() []uint16 {
	m := make([]uint16, 0x10000)
	for i := range m {
		m[i] = uint16(i)
	}
	pos := 0
	for i := 0; i+1 < len(upcaseTable); i += 2 {
		v := binary.LittleEndian.Uint16(upcaseTable[i:])
		if v == 0xFFFF && i+3 < len(upcaseTable) { // run of identity mappings
			pos += int(binary.LittleEndian.Uint16(upcaseTable[i+2:]))
			i += 2
			continue
		}
		if pos < len(m) {
			m[pos] = v
		}
		pos++
	}
	return m
}()

func upcaseChecksum() uint32 {
	var c uint32
	for _, b := range upcaseTable {
		c = (c>>1 | c<<31) + uint32(b)
	}
	return c
}

type exNode struct {
	name     string
	path     string
	isDir    bool
	size     int64
	mtime    time.Time
	children []*exNode
	first    uint32
	clusters uint32
}

// FileSum is a source file's checksum, taken while it was read into an image.
type FileSum struct {
	Sum   uint32
	Size  int64
	MTime time.Time
}

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// ExfatPlan is a fully laid-out image, ready to write.
type ExfatPlan struct {
	Sums map[string]FileSum // filled by Write: checksum of every file as it was read
	// SameDisk: the games and the image share one drive, so Write reads and
	// writes in turns (BatchBytes at a time) instead of both at once.
	SameDisk   bool
	BatchBytes int64

	root         *exNode
	order        []*exNode // every dir and file with data, in cluster order
	clusterSize  int64
	spcShift     uint8
	clusterCount uint32
	fatLength    uint32 // sectors
	heapOffset   uint32 // sectors
	bitmapFirst  uint32
	bitmapBytes  uint64
	upcaseFirst  uint32
	label        string
	TotalBytes   int64 // size of the image file
	DataBytes    int64 // file payload bytes
	Files, Dirs  int
}

var exIgnored = map[string]bool{"thumbs.db": true, "desktop.ini": true, ".ds_store": true}

func scanExfat(dir string, n *exNode, p *ExfatPlan) error {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	sort.Slice(ents, func(i, j int) bool { return strings.ToLower(ents[i].Name()) < strings.ToLower(ents[j].Name()) })
	for _, e := range ents {
		if exIgnored[strings.ToLower(e.Name())] {
			continue
		}
		if len(utf16.Encode([]rune(e.Name()))) > 255 {
			return fmt.Errorf("name too long for exFAT: %s", e.Name())
		}
		fi, err := e.Info()
		if err != nil {
			return err
		}
		c := &exNode{name: e.Name(), path: filepath.Join(dir, e.Name()), mtime: fi.ModTime()}
		switch {
		case e.IsDir():
			c.isDir = true
			p.Dirs++
			if err := scanExfat(c.path, c, p); err != nil {
				return err
			}
		case fi.Mode().IsRegular():
			c.size = fi.Size()
			p.Files++
			p.DataBytes += c.size
		default:
			continue // links, devices etc.
		}
		n.children = append(n.children, c)
	}
	return nil
}

func entrySlots(name string) int { return 2 + (len(utf16.Encode([]rune(name)))+14)/15 }

func dirBytes(n *exNode, isRoot bool) int64 {
	k := 0
	if isRoot {
		k = 3 // volume label + bitmap + up-case
	}
	for _, c := range n.children {
		k += entrySlots(c.name)
	}
	return int64(k+1) * 32 // +1 for the end-of-directory marker
}

func ceilDiv(a, b int64) int64 { return (a + b - 1) / b }

// PlanExfat scans src and lays out the image. src's contents become the
// root of the volume.
func PlanExfat(src, label string) (*ExfatPlan, error) {
	p := &ExfatPlan{root: &exNode{isDir: true, path: src}, label: label}
	if err := scanExfat(src, p.root, p); err != nil {
		return nil, err
	}
	// cluster size: 64K for big-file games, else 32K (same rule as OSFMount script)
	p.clusterSize, p.spcShift = 32768, 6
	if p.Files > 0 && p.DataBytes/int64(p.Files) >= 1<<20 {
		p.clusterSize, p.spcShift = 65536, 7
	}
	cs := p.clusterSize

	// content clusters: up-case, root, dirs, files
	upClusters := uint32(ceilDiv(int64(len(upcaseTable)), cs))
	var content int64
	var walk func(n *exNode, root bool)
	walk = func(n *exNode, root bool) {
		if n.isDir {
			n.clusters = uint32(ceilDiv(dirBytes(n, root), cs))
		} else {
			n.clusters = uint32(ceilDiv(n.size, cs))
		}
		content += int64(n.clusters)
		for _, c := range n.children {
			walk(c, false)
		}
	}
	walk(p.root, true)
	content += int64(upClusters)

	// bitmap size depends on total cluster count, which includes the bitmap
	var bm int64 = 1
	for {
		total := content + bm
		need := ceilDiv(ceilDiv(total, 8), cs)
		if need <= bm {
			break
		}
		bm = need
	}
	total := content + bm
	if total > 0xFFFFFFF5-2 {
		return nil, errors.New("game is too large for a single exFAT image")
	}
	p.clusterCount = uint32(total)
	p.bitmapBytes = uint64(ceilDiv(total, 8))

	// assign clusters in write order
	next := uint32(2)
	p.bitmapFirst = next
	next += uint32(bm)
	p.upcaseFirst = next
	next += upClusters
	var assign func(n *exNode)
	assign = func(n *exNode) {
		if n.clusters > 0 {
			n.first = next
			next += n.clusters
			p.order = append(p.order, n)
		}
		for _, c := range n.children {
			if c.isDir {
				assign(c)
			}
		}
		for _, c := range n.children {
			if !c.isDir {
				assign(c)
			}
		}
	}
	// root first, then directories before files at each level
	p.root.first = next
	next += p.root.clusters
	p.order = append(p.order, p.root)
	rootKids := p.root.children
	for _, c := range rootKids {
		if c.isDir {
			assign(c)
		}
	}
	for _, c := range rootKids {
		if !c.isDir {
			assign(c)
		}
	}

	spc := int64(1) << p.spcShift
	fatBytes := (int64(total) + 2) * 4
	p.fatLength = uint32(ceilDiv(fatBytes, exSector))
	heap := ceilDiv(int64(exFatOffset)+int64(p.fatLength), spc) * spc
	p.heapOffset = uint32(heap)
	p.TotalBytes = (heap + int64(total)*spc) * exSector
	return p, nil
}

// ---------- byte builders ----------

func exTime(t time.Time) (uint32, uint8) {
	t = t.Local()
	if t.Year() < 1980 {
		t = time.Date(1980, 1, 1, 0, 0, 0, 0, time.Local)
	}
	if t.Year() > 2107 {
		t = time.Date(2107, 12, 31, 23, 59, 58, 0, time.Local)
	}
	v := uint32(t.Year()-1980)<<25 | uint32(t.Month())<<21 | uint32(t.Day())<<16 |
		uint32(t.Hour())<<11 | uint32(t.Minute())<<5 | uint32(t.Second()/2)
	return v, uint8((t.Second()%2)*100 + t.Nanosecond()/10000000)
}

func nameHash(u []uint16) uint16 {
	var h uint16
	for _, ch := range u {
		ch = upcaseMap[ch]
		for _, b := range []byte{byte(ch), byte(ch >> 8)} {
			h = (h>>1 | h<<15) + uint16(b)
		}
	}
	return h
}

func setChecksum(set []byte) uint16 {
	var c uint16
	for i, b := range set {
		if i == 2 || i == 3 {
			continue
		}
		c = (c>>1 | c<<15) + uint16(b)
	}
	return c
}

func fileEntrySet(n *exNode, cs int64) []byte {
	u := utf16.Encode([]rune(n.name))
	nameEntries := (len(u) + 14) / 15
	set := make([]byte, (2+nameEntries)*32)
	le := binary.LittleEndian
	// File directory entry
	set[0] = 0x85
	set[1] = byte(1 + nameEntries)
	attr := uint16(exAttrArchive)
	if n.isDir {
		attr = exAttrDir
	}
	le.PutUint16(set[4:], attr)
	ts, ms := exTime(n.mtime)
	le.PutUint32(set[8:], ts)
	le.PutUint32(set[12:], ts)
	le.PutUint32(set[16:], ts)
	set[20], set[21] = ms, ms
	// Stream extension
	s := set[32:64]
	s[0] = 0xC0
	s[1] = 0x01 // AllocationPossible, FAT chain kept valid
	s[3] = byte(len(u))
	le.PutUint16(s[4:], nameHash(u))
	length := uint64(n.size)
	if n.isDir {
		length = uint64(n.clusters) * uint64(cs)
	}
	if n.clusters == 0 {
		length = 0
	}
	le.PutUint64(s[8:], length)   // ValidDataLength
	le.PutUint32(s[20:], n.first) // FirstCluster (0 when empty)
	le.PutUint64(s[24:], length)  // DataLength
	// File name entries
	for i := 0; i < nameEntries; i++ {
		e := set[64+i*32 : 96+i*32]
		e[0] = 0xC1
		for j := 0; j < 15 && i*15+j < len(u); j++ {
			le.PutUint16(e[2+j*2:], u[i*15+j])
		}
	}
	le.PutUint16(set[2:], setChecksum(set))
	return set
}

func (p *ExfatPlan) dirData(n *exNode) []byte {
	buf := make([]byte, 0, int64(n.clusters)*p.clusterSize)
	le := binary.LittleEndian
	if n == p.root {
		e := make([]byte, 32*3)
		lbl := utf16.Encode([]rune(p.label))
		if len(lbl) > 11 {
			lbl = lbl[:11]
		}
		e[0] = 0x83
		e[1] = byte(len(lbl))
		for i, ch := range lbl {
			le.PutUint16(e[2+i*2:], ch)
		}
		b := e[32:64] // allocation bitmap
		b[0] = 0x81
		le.PutUint32(b[20:], p.bitmapFirst)
		le.PutUint64(b[24:], p.bitmapBytes)
		u := e[64:96] // up-case table
		u[0] = 0x82
		le.PutUint32(u[4:], upcaseChecksum())
		le.PutUint32(u[20:], p.upcaseFirst)
		le.PutUint64(u[24:], uint64(len(upcaseTable)))
		buf = append(buf, e...)
	}
	for _, c := range n.children {
		buf = append(buf, fileEntrySet(c, p.clusterSize)...)
	}
	return buf[:cap(buf)] // zero padded; trailing zero = end of directory
}

func (p *ExfatPlan) bootRegion() []byte {
	r := make([]byte, 12*exSector)
	le := binary.LittleEndian
	b := r[:exSector]
	copy(b, []byte{0xEB, 0x76, 0x90})
	copy(b[3:], "EXFAT   ")
	le.PutUint64(b[72:], uint64(p.TotalBytes/exSector))
	le.PutUint32(b[80:], exFatOffset)
	le.PutUint32(b[84:], p.fatLength)
	le.PutUint32(b[88:], p.heapOffset)
	le.PutUint32(b[92:], p.clusterCount)
	le.PutUint32(b[96:], p.root.first)
	le.PutUint32(b[100:], uint32(time.Now().UnixNano()))
	le.PutUint16(b[104:], 0x0100)
	b[108] = 9
	b[109] = p.spcShift
	b[110] = 1    // one FAT
	b[111] = 0x80 // drive select
	b[112] = 100  // percent in use
	for i := 120; i < 510; i++ {
		b[i] = 0xF4
	}
	b[510], b[511] = 0x55, 0xAA
	for s := 1; s <= 8; s++ { // extended boot sectors
		r[s*exSector+510], r[s*exSector+511] = 0x55, 0xAA
	}
	var c uint32
	for i := 0; i < 11*exSector; i++ {
		if i == 106 || i == 107 || i == 112 {
			continue
		}
		c = (c>>1 | c<<31) + uint32(r[i])
	}
	for i := 11 * exSector; i < 12*exSector; i += 4 {
		le.PutUint32(r[i:], c)
	}
	return r
}

// ---------- writer ----------

type ExfatProgress func(done, total int64, current string)

var ErrCancelled = errors.New("cancelled")

type countingWriter struct {
	w    *bufio.Writer
	n    int64
	tick func()
}

func (c *countingWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.n += int64(n)
	return n, err
}

func (c *countingWriter) zeros(n int64) error {
	var z [64 * 1024]byte
	for n > 0 {
		k := n
		if k > int64(len(z)) {
			k = int64(len(z))
		}
		if _, err := c.Write(z[:k]); err != nil {
			return err
		}
		n -= k
	}
	return nil
}

// Write builds the image at dst. If anything fails or is cancelled the
// partial file is removed.
func (p *ExfatPlan) Write(dst string, progress ExfatProgress, cancelled func() bool) (err error) {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() {
		cerr := f.Close()
		if err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(dst)
		}
	}()
	// reserve the whole file up front: less fragmentation, steadier write speed
	f.Truncate(p.TotalBytes)
	w := &countingWriter{w: bufio.NewWriterSize(f, 8<<20)}
	report := func(cur string) {
		if progress != nil {
			progress(w.n, p.TotalBytes, cur)
		}
	}
	le := binary.LittleEndian

	// boot region + backup, then pad to FAT
	boot := p.bootRegion()
	w.Write(boot)
	w.Write(boot)
	w.zeros(int64(exFatOffset)*exSector - w.n)

	// FAT: two reserved entries, then a chain per allocation
	fat := make([]byte, int64(p.fatLength)*exSector)
	le.PutUint32(fat[0:], 0xFFFFFFF8)
	le.PutUint32(fat[4:], exEOC)
	chain := func(first, count uint32) {
		for i := uint32(0); i < count; i++ {
			v := uint32(exEOC)
			if i+1 < count {
				v = first + i + 1
			}
			le.PutUint32(fat[(first+i)*4:], v)
		}
	}
	bmClusters := p.upcaseFirst - p.bitmapFirst
	chain(p.bitmapFirst, bmClusters)
	chain(p.upcaseFirst, uint32(ceilDiv(int64(len(upcaseTable)), p.clusterSize)))
	for _, n := range p.order {
		chain(n.first, n.clusters)
	}
	w.Write(fat)
	w.zeros(int64(p.heapOffset)*exSector - w.n)

	// heap: allocation bitmap (every cluster is in use)
	bm := make([]byte, int64(bmClusters)*p.clusterSize)
	full := p.clusterCount / 8
	for i := uint32(0); i < full; i++ {
		bm[i] = 0xFF
	}
	if r := p.clusterCount % 8; r != 0 {
		bm[full] = byte(1<<r - 1)
	}
	w.Write(bm)
	up := make([]byte, ceilDiv(int64(len(upcaseTable)), p.clusterSize)*p.clusterSize)
	copy(up, upcaseTable)
	w.Write(up)

	// Reading the game files, in image order, one chunk at a time.
	type chunk struct {
		buf []byte
		n   int
		err error
	}
	const chunkSize = 8 << 20
	p.Sums = map[string]FileSum{}
	var sumMu sync.Mutex
	var rd struct {
		idx  int
		src  *os.File
		read int64
		crc  hash.Hash32
	}
	defer func() {
		if rd.src != nil {
			rd.src.Close()
		}
	}()
	// readChunk fills buf with the next piece of file data. ok is false once
	// every file has been read.
	readChunk := func(buf []byte) (c chunk, ok bool) {
		for rd.idx < len(p.order) && (p.order[rd.idx].isDir || p.order[rd.idx].size == 0) {
			rd.idx++
		}
		if rd.idx >= len(p.order) {
			return chunk{}, false
		}
		n := p.order[rd.idx]
		if rd.src == nil {
			src, err := os.Open(n.path)
			if err != nil {
				rd.idx = len(p.order)
				return chunk{err: err}, true
			}
			rd.src, rd.read, rd.crc = src, 0, crc32.New(castagnoli)
		}
		k := int64(len(buf))
		if n.size-rd.read < k {
			k = n.size - rd.read
		}
		got, rerr := io.ReadFull(rd.src, buf[:k])
		rd.crc.Write(buf[:got])
		rd.read += int64(got)
		if rerr != nil {
			rd.idx = len(p.order)
			return chunk{buf: buf, n: got, err: fmt.Errorf("%s changed or couldn't be read while building: %v", n.name, rerr)}, true
		}
		if rd.read >= n.size {
			rd.src.Close()
			rd.src = nil
			sumMu.Lock()
			p.Sums[n.path] = FileSum{Sum: rd.crc.Sum32(), Size: n.size, MTime: n.mtime}
			sumMu.Unlock()
			rd.idx++
		}
		return chunk{buf: buf, n: got}, true
	}
	var nextChunk func() chunk
	var giveBack func([]byte)
	if p.SameDisk {
		// Games and image on one hard disk: reading and writing at the same
		// time makes the disk seek back and forth constantly. Read a big batch
		// into memory, write it all out, and repeat, so the disk does one
		// thing at a time.
		batch := int(p.BatchBytes / chunkSize)
		if batch < 4 {
			batch = 96 // 768 MB
		}
		var free, queue []chunk
		for i := 0; i < batch; i++ {
			free = append(free, chunk{buf: make([]byte, chunkSize)})
		}
		nextChunk = func() chunk {
			if len(queue) == 0 {
				// everything read so far goes to the disk before reading more
				w.w.Flush()
				f.Sync()
				for len(free) > 0 {
					if cancelled != nil && cancelled() {
						break
					}
					c, ok := readChunk(free[len(free)-1].buf)
					if !ok {
						break
					}
					if c.buf != nil {
						free = free[:len(free)-1]
					}
					queue = append(queue, c)
					if c.err != nil {
						break
					}
				}
				if len(queue) == 0 {
					return chunk{err: ErrCancelled}
				}
			}
			c := queue[0]
			queue = queue[1:]
			return c
		}
		giveBack = func(b []byte) { free = append(free, chunk{buf: b}) }
	} else {
		// Different drives: a reader runs ahead of the writer, so both drives
		// stay busy instead of taking turns.
		chunks := make(chan chunk, 6)
		pool := make(chan []byte, 8)
		for i := 0; i < cap(pool); i++ {
			pool <- make([]byte, chunkSize)
		}
		quit := make(chan struct{})
		defer close(quit)
		go func() {
			for {
				var buf []byte
				select {
				case buf = <-pool:
				case <-quit:
					return
				}
				c, ok := readChunk(buf)
				if !ok {
					return
				}
				select {
				case chunks <- c:
				case <-quit:
					return
				}
				if c.err != nil {
					return
				}
			}
		}()
		nextChunk = func() chunk { return <-chunks }
		giveBack = func(b []byte) { pool <- b }
	}
	for _, n := range p.order {
		if cancelled != nil && cancelled() {
			return ErrCancelled
		}
		want := int64(heapPos(p, n.first))
		if w.n != want {
			return fmt.Errorf("internal layout error at %s", n.name)
		}
		if n.isDir {
			if _, err := w.Write(p.dirData(n)); err != nil {
				return err
			}
			continue
		}
		report(n.path)
		var copied int64
		for copied < n.size {
			if cancelled != nil && cancelled() {
				return ErrCancelled
			}
			c := nextChunk()
			if c.n > 0 {
				if _, err := w.Write(c.buf[:c.n]); err != nil {
					return err
				}
				copied += int64(c.n)
				report(n.path)
			}
			if c.buf != nil {
				giveBack(c.buf)
			}
			if c.err != nil {
				return c.err
			}
		}
		if pad := int64(n.clusters)*p.clusterSize - n.size; pad > 0 {
			w.zeros(pad)
		}
	}
	if err := w.w.Flush(); err != nil {
		return err
	}
	if w.n != p.TotalBytes {
		return fmt.Errorf("image size mismatch (%d vs %d)", w.n, p.TotalBytes)
	}
	report("")
	return f.Sync()
}

func heapPos(p *ExfatPlan, cluster uint32) int64 {
	return int64(p.heapOffset)*exSector + int64(cluster-2)*p.clusterSize
}
