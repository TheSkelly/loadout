package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The PS5 keeps the list of what's on its home screen in a database,
// /system_data/priv/mms/app.db. This first step only READS it: Loadout takes
// a copy (kept as a backup on this PC), lists the titles in it, and shows
// which settings the PS5 stores for each one. Nothing on the PS5 is changed.

const homeDBPath = "/system_data/priv/mms/app.db"

type homeTitle struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Flags   map[string]string `json:"flags"` // settings that look like they affect the home screen
	Shown   bool              `json:"shown"` // the PS5 has it marked as visible
	CanHide bool              `json:"canHide"`
	ByUs    bool              `json:"byUs"` // hidden by Loadout, so it can be shown again
	Icon    bool              `json:"icon"` // Loadout has a copy of the tile's picture
}

type homeTable struct {
	Name   string      `json:"name"`
	Cols   []string    `json:"cols"`
	Count  int         `json:"count"`
	Titles []homeTitle `json:"titles"` // only for tables that list titles
	Note   string      `json:"note"`
}

type homeScan struct {
	At      time.Time         `json:"at"`
	Backup  string            `json:"backup"` // folder on this PC holding the copy
	Size    int64             `json:"size"`
	Tables  []homeTable       `json:"tables"`
	Report  string            `json:"report"`  // plain-text details, for working out the next step
	Hidden  map[string]string `json:"hidden"`  // titles Loadout has hidden: id -> name
	CanUndo bool              `json:"canUndo"` // a backup from before Loadout's last change exists
}

var homeFlagWords = []string{"visible", "hidden", "hide", "sort", "order", "folder", "priority", "position", "category", "lastaccess", "pinned", "lock", "installdate", "type"}

func homeCell(v any) string {
	switch x := v.(type) {
	case nil:
		return "(empty)"
	case string:
		if len(x) > 70 {
			return x[:70] + "…"
		}
		return x
	case []byte:
		return fmt.Sprintf("(data, %d bytes)", len(x))
	default:
		return fmt.Sprint(x)
	}
}

func colNamed(cols []string, wants ...string) string {
	for _, w := range wants {
		for _, c := range cols {
			if strings.EqualFold(c, w) {
				return c
			}
		}
	}
	return ""
}

// readHomeDB turns a copy of app.db into the list Loadout shows.
func readHomeDB(main, wal []byte) ([]homeTable, string, error) {
	db, err := openSQLite(main, wal)
	if err != nil {
		return nil, "", err
	}
	tabs, err := db.tables()
	if err != nil {
		return nil, "", err
	}
	sort.Slice(tabs, func(i, j int) bool { return tabs[i].Name < tabs[j].Name })
	var out []homeTable
	var rep strings.Builder
	fmt.Fprintf(&rep, "PS5 home screen database, %d tables, %d bytes (+%d in the log)\n", len(tabs), len(main), len(wal))
	for _, t := range tabs {
		ht := homeTable{Name: t.Name, Cols: t.Cols}
		rows, rerr := db.rows(t, 5000)
		ht.Count = len(rows)
		if rerr != nil {
			ht.Note = rerr.Error()
		}
		fmt.Fprintf(&rep, "\n[%s] %d rows\n  columns: %s\n", t.Name, len(rows), strings.Join(t.Cols, ", "))
		if ht.Note != "" {
			fmt.Fprintf(&rep, "  note: %s\n", ht.Note)
		}
		idCol := colNamed(t.Cols, "titleId", "title_id")
		nameCol := colNamed(t.Cols, "titleName", "appName", "name", "title_name", "canonicalTitle")
		for i, r := range rows {
			if i < 4 {
				var cells []string
				for _, c := range t.Cols {
					cells = append(cells, c+"="+homeCell(r[c]))
				}
				fmt.Fprintf(&rep, "  row: %s\n", strings.Join(cells, " | "))
			}
			if idCol == "" {
				continue
			}
			id, _ := r[idCol].(string)
			if id == "" {
				continue
			}
			ti := homeTitle{ID: id, Flags: map[string]string{}}
			if nameCol != "" {
				ti.Name, _ = r[nameCol].(string)
			}
			for _, c := range t.Cols {
				lc := strings.ToLower(c)
				if c == idCol || c == nameCol {
					continue
				}
				for _, w := range homeFlagWords {
					if strings.Contains(lc, w) {
						if _, blob := r[c].([]byte); !blob {
							ti.Flags[c] = homeCell(r[c])
						}
						break
					}
				}
			}
			if strings.HasPrefix(t.Name, "tbl_iconinfo_") {
				if v, ok := r["visible"].(int64); ok {
					ti.Shown = v == 1
					ti.CanHide = true
				}
			}
			ht.Titles = append(ht.Titles, ti)
		}
		sort.Slice(ht.Titles, func(i, j int) bool {
			return strings.ToLower(ht.Titles[i].Name+ht.Titles[i].ID) < strings.ToLower(ht.Titles[j].Name+ht.Titles[j].ID)
		})
		out = append(out, ht)
	}
	// tables that list titles come first
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].Titles) > len(out[j].Titles) })
	return out, rep.String(), nil
}

func (a *App) homeScan() (*homeScan, error) {
	host, port, _ := a.ps5()
	if host == "" {
		return nil, errors.New("Set your PS5's IP address in Settings first.")
	}
	f, err := ftpDial(host, port)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, ok := f.size(homeDBPath); !ok {
		return nil, errors.New("Loadout couldn't find the home screen database on the PS5 (" + homeDBPath + "). The PS5's FTP server may not allow reading system folders")
	}
	main, err := f.get(homeDBPath, 512<<20)
	if err != nil || len(main) < 100 {
		return nil, fmt.Errorf("couldn't copy the home screen database from the PS5 (%v)", err)
	}
	var wal []byte
	if n, ok := f.size(homeDBPath + "-wal"); ok && n > 0 {
		wal, _ = f.get(homeDBPath+"-wal", 512<<20)
	}
	// keep the copy: it is the backup any later change would be undone from
	dir := filepath.Join(appDir(), "HomeScreen", time.Now().Format("2006-01-02 15.04.05"))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "app.db"), main, 0644); err != nil {
		return nil, err
	}
	if len(wal) > 0 {
		os.WriteFile(filepath.Join(dir, "app.db-wal"), wal, 0644)
	}
	tabs, rep, err := readHomeDB(main, wal)
	if err != nil {
		return nil, fmt.Errorf("the copy was saved to %s, but Loadout couldn't read it: %v", dir, err)
	}
	os.WriteFile(filepath.Join(dir, "details.txt"), []byte(rep), 0644)
	hidden := homeHidden()
	icons := homeIcons(f, main, wal)
	for ti := range tabs {
		for i := range tabs[ti].Titles {
			x := &tabs[ti].Titles[i]
			_, x.ByUs = hidden[x.ID]
			x.Icon = icons[x.ID]
		}
	}
	return &homeScan{At: time.Now(), Backup: dir, Size: int64(len(main) + len(wal)), Tables: tabs, Report: rep, Hidden: hidden, CanUndo: homeLastBefore() != ""}, nil
}

// homeHidden is Loadout's own note of which titles it hid.
func homeHidden() map[string]string {
	m := map[string]string{}
	loadJSON("homehidden.json", &m)
	return m
}

// homeLastBefore finds the newest backup taken just before Loadout changed the database.
func homeLastBefore() string {
	ents, _ := os.ReadDir(filepath.Join(appDir(), "HomeScreen"))
	best := ""
	for _, e := range ents {
		if e.IsDir() && strings.HasSuffix(e.Name(), " before change") && e.Name() > best {
			best = e.Name()
		}
	}
	if best == "" {
		return ""
	}
	return filepath.Join(appDir(), "HomeScreen", best, "app.db")
}

// homeSetShown hides or shows titles on the PS5's home screen by flipping
// their "visible" switch in the database. Each title changes one or two bytes
// in the file and nothing else; a copy from before the change is kept first.
func (a *App) homeSetShown(ids []string, shown bool) (string, error) {
	host, port, _ := a.ps5()
	if host == "" {
		return "", errors.New("Set your PS5's IP address in Settings first.")
	}
	if len(ids) == 0 {
		return "", errors.New("Nothing was picked.")
	}
	f, err := ftpDial(host, port)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var orig, out []byte
	names := map[string]string{}
	done := 0
	for try := 0; ; try++ {
		if n, ok := f.size(homeDBPath + "-wal"); ok && n > 0 {
			return "", errors.New("The PS5 is in the middle of writing its home screen database, so Loadout left it alone. Try again in a minute.")
		}
		orig, err = f.get(homeDBPath, 512<<20)
		if err != nil || len(orig) < 100 {
			return "", fmt.Errorf("couldn't copy the home screen database from the PS5 (%v)", err)
		}
		db, err := openSQLite(orig, nil)
		if err != nil {
			return "", err
		}
		tabs, err := db.tables()
		if err != nil {
			return "", err
		}
		out, done = orig, 0
		for _, t := range tabs {
			if !strings.HasPrefix(t.Name, "tbl_iconinfo_") {
				continue
			}
			user := strings.TrimPrefix(t.Name, "tbl_iconinfo_")
			rows, err := db.rows(t, 5000)
			if err != nil {
				return "", err
			}
			for _, r := range rows {
				id, _ := r["titleId"].(string)
				want := false
				for _, x := range ids {
					want = want || x == id
				}
				if !want {
					continue
				}
				names[id], _ = r["titleName"].(string)
				next, ch, err := flipFlag(out, t.Name, "titleId", id, "visible", shown)
				if err != nil {
					return "", fmt.Errorf("couldn't change %s: %v", id, err)
				}
				out = next
				if ch {
					done++
				}
				// the matching tile entry, when there is one
				if cid, _ := r["localConceptId"].(string); cid != "" {
					if next, _, err := flipFlag(out, "tbl_concepticoninfo_"+user, "localConceptId", cid, "validFlag", shown); err == nil {
						out = next
					}
				}
			}
		}
		if len(names) == 0 {
			return "", errors.New("Those titles aren't in the PS5's home screen list any more. Read the home screen again.")
		}
		// the changed copy must still read cleanly and differ only in those switches
		if len(out) != len(orig) {
			return "", errors.New("internal check failed: the database changed size")
		}
		diff := 0
		for i := range out {
			if out[i] != orig[i] {
				diff++
			}
		}
		if diff > 2*len(ids)*8 {
			return "", errors.New("internal check failed: too much of the database would change")
		}
		if _, _, err := readHomeDB(out, nil); err != nil {
			return "", fmt.Errorf("internal check failed: the changed copy doesn't read back (%v)", err)
		}
		if diff == 0 {
			break
		}
		// if the PS5 changed its database while we worked, start over on the new one
		again, err := f.get(homeDBPath, 512<<20)
		if err != nil {
			return "", err
		}
		if string(again) == string(orig) {
			break
		}
		if try >= 3 {
			return "", errors.New("The PS5 keeps changing its home screen database right now. Close any game or download on the PS5 and try again.")
		}
	}
	if string(out) != string(orig) {
		dir := filepath.Join(appDir(), "HomeScreen", time.Now().Format("2006-01-02 15.04.05")+" before change")
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, "app.db"), orig, 0644); err != nil {
			return "", fmt.Errorf("couldn't save the backup first, so nothing was changed: %v", err)
		}
		if err := f.put(homeDBPath, out); err != nil {
			return "", fmt.Errorf("the PS5 didn't take the change (%v). Its database was not replaced", err)
		}
		if back, err := f.get(homeDBPath, 512<<20); err != nil || string(back) != string(out) {
			// put the original back rather than leave something half-written
			f.put(homeDBPath, orig)
			return "", errors.New("The change didn't land on the PS5 cleanly, so Loadout put the original database back. Nothing was changed.")
		}
	}
	hidden := homeHidden()
	for id, n := range names {
		if shown {
			delete(hidden, id)
		} else {
			hidden[id] = n
		}
	}
	saveJSON("homehidden.json", hidden)
	var list []string
	for _, n := range names {
		list = append(list, n)
	}
	sort.Strings(list)
	word := map[bool]string{true: "Showing again", false: "Hidden"}[shown]
	return fmt.Sprintf("%s: %s. Restart the PS5 now (a full restart, not rest mode) so it picks the change up.", word, strings.Join(list, ", ")), nil
}

// homeUndo puts back the copy taken before Loadout's last change.
func (a *App) homeUndo() (string, error) {
	p := homeLastBefore()
	if p == "" {
		return "", errors.New("There's no backup from before a change yet.")
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	if _, _, err := readHomeDB(data, nil); err != nil {
		return "", fmt.Errorf("that backup doesn't read as a database, so it wasn't sent: %v", err)
	}
	host, port, _ := a.ps5()
	f, err := ftpDial(host, port)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := f.put(homeDBPath, data); err != nil {
		return "", fmt.Errorf("the PS5 didn't take the backup: %v", err)
	}
	saveJSON("homehidden.json", map[string]string{})
	return "The backup from before Loadout's last change is back on the PS5. Restart the PS5 now.", nil
}

func (a *App) homeRoutes(api func(string, func(r *http.Request) (any, error)), body func(*http.Request, any)) {
	api("/api/home/scan", func(r *http.Request) (any, error) {
		return a.homeScan()
	})
	api("/api/home/show", func(r *http.Request) (any, error) {
		var b struct {
			IDs   []string
			Shown bool
		}
		body(r, &b)
		msg, err := a.homeSetShown(b.IDs, b.Shown)
		return map[string]string{"msg": msg}, err
	})
	api("/api/home/undo", func(r *http.Request) (any, error) {
		msg, err := a.homeUndo()
		return map[string]string{"msg": msg}, err
	})
	api("/api/home/copy", func(r *http.Request) (any, error) {
		var b struct{ Text string }
		body(r, &b)
		return nil, copyText(b.Text)
	})
	api("/api/home/open", func(r *http.Request) (any, error) {
		var b struct{ Dir string }
		body(r, &b)
		root := filepath.Join(appDir(), "HomeScreen")
		if !strings.HasPrefix(filepath.Clean(b.Dir), root) {
			return nil, errors.New("That isn't a Loadout backup folder.")
		}
		return nil, openPath(b.Dir, false)
	})
}

func homeIconPath(id string) string {
	return filepath.Join(appDir(), "HomeScreen", "icons", id+".png")
}

// homeIcons copies each tile's picture from the PS5 (once; later reads reuse
// the copy) and reports which tiles have one.
func homeIcons(f *ftpConn, main, wal []byte) map[string]bool {
	have := map[string]bool{}
	db, err := openSQLite(main, wal)
	if err != nil {
		return have
	}
	tabs, _ := db.tables()
	os.MkdirAll(filepath.Join(appDir(), "HomeScreen", "icons"), 0755)
	for _, t := range tabs {
		if t.Name != "tbl_contentinfo" {
			continue
		}
		rows, _ := db.rows(t, 5000)
		for _, r := range rows {
			id, _ := r["titleId"].(string)
			p, _ := r["icon0Info"].(string)
			if gameID(id) != id || !strings.HasPrefix(p, "/") {
				continue
			}
			if i := strings.IndexByte(p, '?'); i >= 0 {
				p = p[:i]
			}
			dst := homeIconPath(id)
			if fi, err := os.Stat(dst); err == nil && fi.Size() > 0 {
				have[id] = true
				continue
			}
			if b, err := f.get(p, 8<<20); err == nil && len(b) > 64 {
				if os.WriteFile(dst, b, 0644) == nil {
					have[id] = true
				}
			}
		}
	}
	return have
}
