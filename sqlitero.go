package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
)

// A small read-only reader for SQLite database files, enough to list the
// tables, their columns and their rows. It never writes. It understands the
// write-ahead log (the "-wal" file) so recent changes are seen too.

type sqliteDB struct {
	main   []byte
	wal    map[uint32][]byte // pages the write-ahead log overrides
	psize  int
	usable int
	npages uint32
	// where the row most recently handed out by walk sits in the file
	curPage  uint32
	curOff   int // offset of the row's data inside that page
	curLocal int // how much of the row is on that page
}

type sqliteTable struct {
	Name   string
	Cols   []string
	root   uint32
	rowid  int  // column that is the row id itself (-1 if none)
	noRead bool // a kind of table this reader can't walk
}

var errNotSQLite = errors.New("that file isn't a database this reader understands")

func openSQLite(main, wal []byte) (*sqliteDB, error) {
	if len(main) < 100 || string(main[:16]) != "SQLite format 3\x00" {
		return nil, errNotSQLite
	}
	ps := int(binary.BigEndian.Uint16(main[16:18]))
	if ps == 1 {
		ps = 65536
	}
	if ps < 512 || ps&(ps-1) != 0 {
		return nil, errNotSQLite
	}
	if enc := binary.BigEndian.Uint32(main[56:60]); enc > 1 {
		return nil, errors.New("the database stores text in a way this reader doesn't handle")
	}
	db := &sqliteDB{main: main, psize: ps, usable: ps - int(main[20]), wal: map[uint32][]byte{}}
	db.npages = uint32(len(main) / ps)
	db.readWAL(wal)
	return db, nil
}

// readWAL applies the committed part of a write-ahead log.
func (db *sqliteDB) readWAL(w []byte) {
	if len(w) < 32 {
		return
	}
	magic := binary.BigEndian.Uint32(w[0:4])
	if magic != 0x377f0682 && magic != 0x377f0683 {
		return
	}
	if int(binary.BigEndian.Uint32(w[8:12])) != db.psize {
		return
	}
	salt := string(w[16:24])
	pending := map[uint32][]byte{}
	for off := 32; off+24+db.psize <= len(w); off += 24 + db.psize {
		h := w[off : off+24]
		if string(h[8:16]) != salt {
			break
		}
		pg := binary.BigEndian.Uint32(h[0:4])
		pending[pg] = w[off+24 : off+24+db.psize]
		if size := binary.BigEndian.Uint32(h[4:8]); size != 0 { // a commit
			for k, v := range pending {
				db.wal[k] = v
			}
			pending = map[uint32][]byte{}
			if size > db.npages {
				db.npages = size
			}
		}
	}
}

func (db *sqliteDB) page(n uint32) ([]byte, error) {
	if n == 0 {
		return nil, errors.New("bad page number")
	}
	if p, ok := db.wal[n]; ok {
		return p, nil
	}
	off := int(n-1) * db.psize
	if off+db.psize > len(db.main) {
		return nil, errors.New("the database file is cut short")
	}
	return db.main[off : off+db.psize], nil
}

func varint(b []byte) (int64, int) {
	var v uint64
	for i := 0; i < 9 && i < len(b); i++ {
		if i == 8 {
			return int64(v<<8 | uint64(b[i])), 9
		}
		v = v<<7 | uint64(b[i]&0x7f)
		if b[i]&0x80 == 0 {
			return int64(v), i + 1
		}
	}
	return 0, 0
}

// walk calls fn for every row of the table b-tree at root. fn returns false to stop.
func (db *sqliteDB) walk(root uint32, depth int, fn func(rowid int64, payload []byte) bool) (bool, error) {
	if depth > 40 {
		return false, errors.New("the database is tangled")
	}
	p, err := db.page(root)
	if err != nil {
		return false, err
	}
	h := 0
	if root == 1 {
		h = 100
	}
	kind := p[h]
	n := int(binary.BigEndian.Uint16(p[h+3 : h+5]))
	switch kind {
	case 5: // inside page: pointers to children
		for i := 0; i < n; i++ {
			c := int(binary.BigEndian.Uint16(p[h+12+2*i:]))
			if c+4 > len(p) {
				return false, errors.New("damaged page")
			}
			more, err := db.walk(binary.BigEndian.Uint32(p[c:]), depth+1, fn)
			if err != nil || !more {
				return more, err
			}
		}
		return db.walk(binary.BigEndian.Uint32(p[h+8:]), depth+1, fn)
	case 13: // leaf: the rows
		for i := 0; i < n; i++ {
			c := int(binary.BigEndian.Uint16(p[h+8+2*i:]))
			if c >= len(p) {
				return false, errors.New("damaged page")
			}
			size, a := varint(p[c:])
			rowid, b := varint(p[c+a:])
			c += a + b
			if a == 0 || b == 0 || size < 0 || size > 64<<20 {
				return false, errors.New("damaged row")
			}
			P, U := int(size), db.usable
			local := P
			if X := U - 35; P > X {
				M := (U-12)*32/255 - 23
				local = M + (P-M)%(U-4)
				if local > X {
					local = M
				}
			}
			if c+local > len(p) {
				return false, errors.New("damaged row")
			}
			payload := p[c : c+local]
			if local < P {
				payload = append([]byte(nil), payload...)
				next := binary.BigEndian.Uint32(p[c+local:])
				for len(payload) < P && next != 0 {
					op, err := db.page(next)
					if err != nil {
						return false, err
					}
					take := P - len(payload)
					if take > U-4 {
						take = U - 4
					}
					payload = append(payload, op[4:4+take]...)
					next = binary.BigEndian.Uint32(op[:4])
				}
				if len(payload) < P {
					return false, errors.New("damaged row")
				}
			}
			db.curPage, db.curOff, db.curLocal = root, c, local
			if !fn(rowid, payload) {
				return false, nil
			}
		}
		return true, nil
	}
	return false, errors.New("unexpected kind of page")
}

// record decodes one row into its values: nil, int64, float64, string or []byte.
func sqliteRecord(b []byte) []any {
	hs, n := varint(b)
	if n == 0 || int(hs) > len(b) {
		return nil
	}
	head, body := b[n:hs], b[hs:]
	var out []any
	for len(head) > 0 {
		t, k := varint(head)
		if k == 0 {
			break
		}
		head = head[k:]
		size := 0
		switch {
		case t >= 1 && t <= 4:
			size = int(t)
		case t == 5:
			size = 6
		case t == 6, t == 7:
			size = 8
		case t >= 12:
			size = int(t-12) / 2
		}
		if size > len(body) {
			break
		}
		v := body[:size]
		body = body[size:]
		switch {
		case t == 0:
			out = append(out, nil)
		case t >= 1 && t <= 6:
			var x int64
			if v[0]&0x80 != 0 {
				x = -1
			}
			for _, c := range v {
				x = x<<8 | int64(c)
			}
			out = append(out, x)
		case t == 7:
			out = append(out, math.Float64frombits(binary.BigEndian.Uint64(v)))
		case t == 8:
			out = append(out, int64(0))
		case t == 9:
			out = append(out, int64(1))
		case t >= 12 && t%2 == 0:
			out = append(out, append([]byte(nil), v...))
		case t >= 13:
			out = append(out, string(v))
		default:
			out = append(out, nil)
		}
	}
	return out
}

// sqliteColumns pulls the column names out of a CREATE TABLE statement.
func sqliteColumns(sql string) (cols []string, rowid int) {
	rowid = -1
	a, b := strings.Index(sql, "("), strings.LastIndex(sql, ")")
	if a < 0 || b <= a {
		return nil, -1
	}
	var parts []string
	depth, start, quote := 0, a+1, byte(0)
	for i := a + 1; i < b; i++ {
		c := sql[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			quote = c
		case c == '[':
			quote = ']'
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ',' && depth == 0:
			parts = append(parts, sql[start:i])
			start = i + 1
		}
	}
	parts = append(parts, sql[start:b])
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		up := strings.ToUpper(p)
		skip := false
		for _, k := range []string{"PRIMARY KEY", "UNIQUE", "CHECK", "FOREIGN KEY", "CONSTRAINT"} {
			if strings.HasPrefix(up, k) {
				skip = true
			}
		}
		if skip {
			continue
		}
		name := p
		switch p[0] {
		case '"', '`', '\'':
			if e := strings.IndexByte(p[1:], p[0]); e >= 0 {
				name = p[1 : 1+e]
			}
		case '[':
			if e := strings.IndexByte(p, ']'); e > 0 {
				name = p[1:e]
			}
		default:
			if e := strings.IndexAny(p, " \t\r\n"); e > 0 {
				name = p[:e]
			}
		}
		if strings.Contains(up, "INTEGER PRIMARY KEY") && !strings.Contains(up, "DESC") {
			rowid = len(cols)
		}
		cols = append(cols, name)
	}
	return cols, rowid
}

// tables lists every table in the database.
func (db *sqliteDB) tables() ([]sqliteTable, error) {
	var out []sqliteTable
	_, err := db.walk(1, 0, func(_ int64, payload []byte) bool {
		r := sqliteRecord(payload)
		if len(r) < 5 {
			return true
		}
		typ, _ := r[0].(string)
		name, _ := r[1].(string)
		root, _ := r[3].(int64)
		sql, _ := r[4].(string)
		if typ != "table" || strings.HasPrefix(name, "sqlite_") {
			return true
		}
		t := sqliteTable{Name: name, root: uint32(root)}
		t.Cols, t.rowid = sqliteColumns(sql)
		if strings.Contains(strings.ToUpper(sql), "WITHOUT ROWID") || strings.HasPrefix(strings.ToUpper(strings.TrimSpace(sql)), "CREATE VIRTUAL") || root <= 0 {
			t.noRead = true
		}
		out = append(out, t)
		return true
	})
	return out, err
}

// rows reads up to max rows of a table, each as column name -> value.
func (db *sqliteDB) rows(t sqliteTable, max int) ([]map[string]any, error) {
	if t.noRead {
		return nil, fmt.Errorf("table %s is a kind this reader can't open", t.Name)
	}
	var out []map[string]any
	_, err := db.walk(t.root, 0, func(rowid int64, payload []byte) bool {
		r := sqliteRecord(payload)
		m := make(map[string]any, len(t.Cols))
		for i, c := range t.Cols {
			switch {
			case i == t.rowid:
				m[c] = rowid
			case i < len(r):
				m[c] = r[i]
			default:
				m[c] = nil
			}
		}
		out = append(out, m)
		return len(out) < max
	})
	return out, err
}

// flipFlag returns a copy of the database file with one yes/no value changed:
// in table, on the row where keyCol equals key, column col is set to on.
// It only ever changes a single byte, and only when the value is stored as a
// plain 0 or 1, so the file's layout cannot be damaged by it. changed is
// false when the value was already what was asked for.
func flipFlag(main []byte, table, keyCol, key, col string, on bool) (out []byte, changed bool, err error) {
	db, err := openSQLite(main, nil)
	if err != nil {
		return nil, false, err
	}
	tabs, err := db.tables()
	if err != nil {
		return nil, false, err
	}
	for _, t := range tabs {
		if t.Name != table || t.noRead {
			continue
		}
		ki, ci := -1, -1
		for i, c := range t.Cols {
			if c == keyCol {
				ki = i
			}
			if c == col {
				ci = i
			}
		}
		if ki < 0 || ci < 0 || ci == t.rowid {
			return nil, false, fmt.Errorf("table %s doesn't have the expected columns", table)
		}
		at := -1
		var werr error
		_, err := db.walk(t.root, 0, func(_ int64, payload []byte) bool {
			r := sqliteRecord(payload)
			if ki >= len(r) || ci >= len(r) {
				return true
			}
			if k, _ := r[ki].(string); k != key {
				return true
			}
			hs, n := varint(payload)
			pos := n
			for i := 0; i < ci; i++ {
				_, k := varint(payload[pos:hs])
				if k == 0 {
					werr = errors.New("damaged row")
					return false
				}
				pos += k
			}
			if pos >= int(hs) || pos >= db.curLocal {
				werr = errors.New("the value isn't where it can be changed safely")
				return false
			}
			if b := payload[pos]; b != 8 && b != 9 {
				werr = fmt.Errorf("the value isn't a plain yes/no (it is stored as kind %d)", b)
				return false
			}
			at = int(db.curPage-1)*db.psize + db.curOff + pos
			return false
		})
		if err != nil {
			return nil, false, err
		}
		if werr != nil {
			return nil, false, werr
		}
		if at < 0 {
			return nil, false, fmt.Errorf("%s isn't in %s", key, table)
		}
		want := byte(8)
		if on {
			want = 9
		}
		if main[at] == want {
			return main, false, nil
		}
		out = append([]byte(nil), main...)
		out[at] = want
		return out, true, nil
	}
	return nil, false, fmt.Errorf("table %s isn't in the database", table)
}
