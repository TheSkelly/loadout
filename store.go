package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"image"
	"image/draw"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Storefront look: big hero art from each game's own files (sce_sys/pic0.png)
// plus publisher, genre, release date, rating, description and screenshots
// from the game's public PlayStation Store listing. Everything is saved on the
// PC so it's fetched once and works offline afterwards.

// StoreInfo is what the PlayStation Store says about a game.
type StoreInfo struct {
	Name      string
	Publisher string
	Release   string // YYYY-MM-DD
	Genres    []string
	Desc      string
	Rating    float64 // 0..5
	Ratings   int
	Rated     string // "ESRB Mature", "PEGI 18"
	URL       string // store page
	Shots     []string
	Fetched   time.Time
	Err       string // why nothing was found, for the UI

	About      string `json:",omitempty"` // plain English summary (Wikipedia)
	AboutURL   string `json:",omitempty"`
	AboutTried bool   `json:",omitempty"`
}

func artDir() string { return filepath.Join(appDir(), "art") }

func safeKey(k string) string {
	return strings.NewReplacer(":", "_", "\\", "_", "/", "_", " ", "_").Replace(k)
}

// ---------- images ----------

// saveJPEG scales an image to fit maxW wide and writes it as a JPEG.
func saveJPEG(img image.Image, dst string, maxW int) error {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > maxW {
		h = h * maxW / w
		w = maxW
	}
	src := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(src, src.Bounds(), img, b.Min, draw.Src)
	out := src
	if w != b.Dx() {
		out = image.NewRGBA(image.Rect(0, 0, w, h))
		sw, sh := b.Dx(), b.Dy()
		for y := 0; y < h; y++ {
			y0, y1 := y*sh/h, (y+1)*sh/h
			if y1 <= y0 {
				y1 = y0 + 1
			}
			for x := 0; x < w; x++ {
				x0, x1 := x*sw/w, (x+1)*sw/w
				if x1 <= x0 {
					x1 = x0 + 1
				}
				var r, g, bl, n int
				for yy := y0; yy < y1; yy++ {
					i := yy*src.Stride + x0*4
					for xx := x0; xx < x1; xx++ {
						r += int(src.Pix[i])
						g += int(src.Pix[i+1])
						bl += int(src.Pix[i+2])
						n++
						i += 4
					}
				}
				o := y*out.Stride + x*4
				out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = uint8(r/n), uint8(g/n), uint8(bl/n), 255
			}
		}
	}
	os.MkdirAll(filepath.Dir(dst), 0755)
	f, err := os.Create(dst + ".tmp")
	if err != nil {
		return err
	}
	err = jpeg.Encode(f, out, &jpeg.Options{Quality: 84})
	f.Close()
	if err != nil {
		os.Remove(dst + ".tmp")
		return err
	}
	return os.Rename(dst+".tmp", dst)
}

func saveJPEGFile(src, dst string, maxW int) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	img, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return err
	}
	return saveJPEG(img, dst, maxW)
}

// findSceSys finds a game's sce_sys folder in its folder or its -app folder.
func findSceSys(folder string) string {
	if st, err := os.Stat(filepath.Join(folder, "sce_sys")); err == nil && st.IsDir() {
		return filepath.Join(folder, "sce_sys")
	}
	ents, _ := os.ReadDir(folder)
	for _, e := range ents {
		if e.IsDir() {
			p := filepath.Join(folder, e.Name(), "sce_sys")
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				return p
			}
		}
	}
	return ""
}

// ---------- store lookups ----------

var storeHTTP = &http.Client{Timeout: 20 * time.Second}

func getBody(cl *http.Client, u string, max int64) ([]byte, int, error) {
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130 Safari/537.36")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := cl.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, max))
	return b, resp.StatusCode, err
}

var tagRe = regexp.MustCompile(`(?s)<[^>]+>`)

func cleanText(s string) string {
	s = strings.NewReplacer("<br>", "\n", "<br/>", "\n", "<br />", "\n", "<BR>", "\n").Replace(s)
	s = html.UnescapeString(tagRe.ReplaceAllString(s, ""))
	lines := strings.Split(s, "\n")
	var out []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// storePS4 reads the old store catalog, which still covers PS4 titles.
func storePS4(cl *http.Client, id string) (*StoreInfo, error) {
	for _, reg := range storeRegions {
		b, code, err := getBody(cl, storeBase+"/store/api/chihiro/00_09_000/titlecontainer/"+reg+"/999/"+id+"_00", 8<<20)
		if err != nil {
			return nil, err
		}
		if code != 200 {
			continue
		}
		var d struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			Provider     string `json:"provider_name"`
			Release      string `json:"release_date"`
			LongDesc     string `json:"long_desc"`
			ContentRated struct {
				Description string `json:"description"`
			} `json:"content_rating"`
			Star struct {
				Score string `json:"score"`
				Total string `json:"total"`
			} `json:"star_rating"`
			Metadata struct {
				Genre struct {
					Values []string `json:"values"`
				} `json:"genre"`
			} `json:"metadata"`
			MediaList struct {
				Screenshots []struct {
					URL string `json:"url"`
				} `json:"screenshots"`
			} `json:"mediaList"`
		}
		if json.Unmarshal(b, &d) != nil || d.Name == "" {
			continue
		}
		si := &StoreInfo{Name: d.Name, Publisher: d.Provider, Genres: d.Metadata.Genre.Values, Desc: cleanText(d.LongDesc), Rated: d.ContentRated.Description}
		if len(d.Release) >= 10 {
			si.Release = d.Release[:10]
		}
		si.Rating, _ = strconv.ParseFloat(d.Star.Score, 64)
		si.Ratings, _ = strconv.Atoi(d.Star.Total)
		for _, s := range d.MediaList.Screenshots {
			if strings.HasPrefix(s.URL, "http") {
				si.Shots = append(si.Shots, s.URL)
			}
		}
		lang := strings.ToLower(strings.Replace(reg, "/", "-", 1))
		parts := strings.SplitN(reg, "/", 2)
		if len(parts) == 2 {
			lang = parts[1] + "-" + strings.ToLower(parts[0])
		}
		if d.ID != "" {
			si.URL = "https://store.playstation.com/" + lang + "/product/" + d.ID
		}
		return si, nil
	}
	return nil, errors.New("not in the PlayStation Store catalog")
}

var (
	jsonScriptRe = regexp.MustCompile(`(?s)<script[^>]*type="application/(?:ld\+)?json"[^>]*>(.*?)</script>`)
	qaRe         = func(qa string) *regexp.Regexp {
		return regexp.MustCompile(`(?s)data-qa="` + regexp.QuoteMeta(qa) + `"[^>]*>(.*?)</`)
	}
	qaName    = qaRe("mfe-game-title#name")
	qaPub     = qaRe("mfe-game-title#publisher")
	qaRelease = qaRe("gameInfo#releaseInformation#releaseDate-value")
	qaGenre   = qaRe("gameInfo#releaseInformation#genre-value")
	qaDesc    = regexp.MustCompile(`(?s)data-qa="mfe-game-overview#description"[^>]*>(.*?)</p>`)
	qaStars   = qaRe("mfe-star-rating#overall-rating#average-rating")
	vulcanRe  = regexp.MustCompile(`https://image\.api\.playstation\.com/vulcan/[A-Za-z0-9_/\-.]+?\.(?:jpg|jpeg|png)`)
)

// locale picks the store region from a content ID's prefix (UP = US, EP = Europe...).
func storeLocale(contentID string) string {
	switch strings.ToUpper(contentID[:min(2, len(contentID))]) {
	case "EP":
		return "en-gb"
	case "JP":
		return "ja-jp"
	case "HP":
		return "en-hk"
	case "KP":
		return "ko-kr"
	}
	return "en-us"
}

// walkJSON visits every object in a decoded JSON tree.
func walkJSON(v any, fn func(map[string]any)) {
	switch t := v.(type) {
	case map[string]any:
		fn(t)
		for _, x := range t {
			walkJSON(x, fn)
		}
	case []any:
		for _, x := range t {
			walkJSON(x, fn)
		}
	}
}

func str(m map[string]any, k string) string {
	if s, ok := m[k].(string); ok {
		return s
	}
	return ""
}

// storePS5 reads a PS5 game's store product page.
func storePS5(cl *http.Client, contentID string) (*StoreInfo, error) {
	var page []byte
	var url, usedLoc string
	for _, loc := range []string{storeLocale(contentID), "en-us", "en-gb"} {
		u := storeWeb + "/" + loc + "/product/" + contentID
		b, code, err := getBody(cl, u, 16<<20)
		if err != nil {
			return nil, err
		}
		if code == 200 && len(b) > 2000 {
			page, url, usedLoc = b, "https://store.playstation.com/"+loc+"/product/"+contentID, loc
			break
		}
	}
	if page == nil {
		return nil, errors.New("no store page for this game")
	}
	si := &StoreInfo{URL: url}
	shots := map[string]bool{}
	addShot := func(u string) {
		if u != "" && !shots[u] {
			shots[u] = true
			si.Shots = append(si.Shots, u)
		}
	}
	// 1) the JSON the page is built from
	for _, m := range jsonScriptRe.FindAllSubmatch(page, -1) {
		var v any
		if json.Unmarshal(m[1], &v) != nil {
			continue
		}
		walkJSON(v, func(o map[string]any) {
			if p := str(o, "publisherName"); p != "" && si.Publisher == "" {
				si.Publisher = p
				if n := str(o, "name"); n != "" && si.Name == "" {
					si.Name = n
				}
				if r := str(o, "releaseDate"); len(r) >= 10 && si.Release == "" {
					si.Release = r[:10]
				}
			}
			if gs, ok := o["localizedGenres"].([]any); ok && len(si.Genres) == 0 {
				for _, g := range gs {
					if gm, ok := g.(map[string]any); ok && str(gm, "value") != "" {
						si.Genres = append(si.Genres, str(gm, "value"))
					}
				}
			}
			if ds, ok := o["descriptions"].([]any); ok && si.Desc == "" {
				for _, d := range ds {
					if dm, ok := d.(map[string]any); ok && strings.EqualFold(str(dm, "type"), "LONG") {
						si.Desc = cleanText(str(dm, "value"))
					}
				}
			}
			if sr, ok := o["starRating"].(map[string]any); ok && si.Rating == 0 {
				if f, ok := sr["averageRating"].(float64); ok {
					si.Rating = f
				}
				if f, ok := sr["totalRatingsCount"].(float64); ok {
					si.Ratings = int(f)
				}
			}
			if role := strings.ToUpper(str(o, "role")); role == "SCREENSHOT" && strings.HasPrefix(str(o, "url"), "http") {
				addShot(str(o, "url"))
			}
			if si.Desc == "" && str(o, "@type") != "" && str(o, "description") != "" {
				si.Desc = cleanText(str(o, "description"))
			}
		})
	}
	// 2) the rendered page itself
	grab := func(re *regexp.Regexp) string {
		if m := re.FindSubmatch(page); m != nil {
			return cleanText(string(m[1]))
		}
		return ""
	}
	if si.Name == "" {
		si.Name = grab(qaName)
	}
	if si.Publisher == "" {
		si.Publisher = grab(qaPub)
	}
	if si.Release == "" {
		if r := grab(qaRelease); r != "" {
			layouts := []string{"2/1/2006", "1/2/2006", "2006-01-02", "2.1.2006", "2006/1/2"}
			if usedLoc == "en-us" {
				layouts = []string{"1/2/2006", "2006-01-02", "2/1/2006"} // US pages put the month first
			}
			for _, layout := range layouts {
				if t, err := time.Parse(layout, r); err == nil {
					si.Release = t.Format("2006-01-02")
					break
				}
			}
		}
	}
	if len(si.Genres) == 0 {
		for _, g := range strings.Split(grab(qaGenre), ",") {
			if g = strings.TrimSpace(g); g != "" {
				si.Genres = append(si.Genres, g)
			}
		}
	}
	if si.Desc == "" {
		si.Desc = grab(qaDesc)
	}
	if si.Rating == 0 {
		si.Rating, _ = strconv.ParseFloat(grab(qaStars), 64)
	}
	// 3) screenshots: page images after the first (the cover), skipping rating badges
	if len(si.Shots) == 0 {
		for i, u := range vulcanRe.FindAllString(string(page), -1) {
			if i > 0 {
				addShot(u)
			}
		}
	}
	if si.Name == "" && si.Publisher == "" && si.Desc == "" {
		return nil, errors.New("couldn't read the store page")
	}
	return si, nil
}

var storeWeb = "https://store.playstation.com"

func init() {
	// lets the dev build talk to a stand-in store for testing
	if b := os.Getenv("TT_STOREBASE"); b != "" {
		storeBase, storeWeb = b, b
	}
}

// lookupStore finds store info for a library entry.
func lookupStore(e *LibEntry) (*StoreInfo, error) {
	var si *StoreInfo
	var err error
	if strings.HasPrefix(e.TitleID, "CUSA") {
		si, err = storePS4(storeHTTP, e.TitleID)
	} else if e.ContentID != "" {
		si, err = storePS5(storeHTTP, e.ContentID)
		if err != nil && e.TitleID != "" {
			// some PS5 games are still in the old catalog too
			if si2, err2 := storePS4(storeHTTP, e.TitleID); err2 == nil {
				si, err = si2, nil
			}
		}
	} else if e.TitleID != "" {
		si, err = storePS4(storeHTTP, e.TitleID)
	} else {
		err = errors.New("no title ID to look up")
	}
	return si, err
}

// downloadShots saves up to n screenshots locally and returns their paths.
func downloadShots(urls []string, key string, n int) []string {
	var out []string
	for i, u := range urls {
		if len(out) >= n {
			break
		}
		src := u
		if strings.Contains(u, "image.api.playstation.com") && !strings.Contains(u, "?") {
			src = u + "?w=1280"
		}
		b, code, err := getBody(storeHTTP, src, 12<<20)
		if err != nil || code != 200 {
			continue
		}
		img, _, err := image.Decode(strings.NewReader(string(b)))
		if err != nil {
			continue
		}
		dst := filepath.Join(artDir(), fmt.Sprintf("%s_shot%d.jpg", safeKey(key), i))
		if saveJPEG(img, dst, 1280) == nil {
			out = append(out, dst)
		}
	}
	return out
}

// ---------- the art worker ----------

var (
	artTriedMu sync.Mutex
	artTried   = map[string]bool{} // keys looked at this session
)

// artPass fills in hero art and store info for games that don't have them yet.
func (a *App) artPass(force string) {
	a.mu.Lock()
	enabled := boolOr(a.cfg.StoreInfo, true)
	type job struct {
		key string
		e   LibEntry
	}
	var jobs []job
	for k, e := range a.lib.Games {
		artTriedMu.Lock()
		tried := artTried[k]
		artTriedMu.Unlock()
		if force != "" && k != force {
			continue
		}
		if tried && force == "" {
			continue
		}
		jobs = append(jobs, job{k, *e})
	}
	a.mu.Unlock()
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].e.Name < jobs[j].e.Name })
	for _, j := range jobs {
		artTriedMu.Lock()
		artTried[j.key] = true
		artTriedMu.Unlock()
		e := j.e
		hero := e.Hero
		if (hero == "" || !fileExists(hero) || force != "") && e.Present && e.Pkg {
			// PS4 packages keep their background art inside the file
			if img, _ := pkgImage(e.Folder, pkgEntryPic1, pkgEntryPic0); img != nil {
				dst := filepath.Join(artDir(), safeKey(j.key)+"_hero.jpg")
				if saveJPEG(img, dst, 1600) == nil {
					hero = dst
				}
			}
		} else if (hero == "" || !fileExists(hero) || force != "") && e.Present {
			if sys := findSceSys(e.Folder); sys != "" {
				for _, n := range []string{"pic0.png", "pic1.png"} {
					p := filepath.Join(sys, n)
					if fileExists(p) {
						dst := filepath.Join(artDir(), safeKey(j.key)+"_hero.jpg")
						if saveJPEGFile(p, dst, 1600) == nil {
							hero = dst
						}
						break
					}
				}
			}
		}
		var si *StoreInfo
		if enabled && (e.Store == nil || force != "") {
			info, err := lookupStore(&e)
			if err != nil {
				si = &StoreInfo{Fetched: time.Now(), Err: err.Error()}
				var ne interface{ Timeout() bool }
				if errors.As(err, &ne) || strings.Contains(err.Error(), "dial") || strings.Contains(err.Error(), "no such host") {
					si = nil // offline: try again another time
				}
			} else {
				info.Fetched = time.Now()
				info.Shots = downloadShots(info.Shots, j.key, 6)
				si = info
			}
		}
		// a plain English description, once per game
		cur := si
		if cur == nil {
			cur = e.Store
		}
		if enabled && e.Name != "" && (cur == nil || !cur.AboutTried || force != "") {
			if about, link, err := wikiAbout(e.Name); err == nil {
				n := StoreInfo{}
				if cur != nil {
					n = *cur
				} else {
					n.Fetched = time.Now()
				}
				n.About, n.AboutURL, n.AboutTried = about, link, true
				si = &n
			}
		}
		a.mu.Lock()
		if le := a.lib.Games[j.key]; le != nil {
			if hero != "" {
				le.Hero = hero
			}
			if si != nil {
				le.Store = si
			}
			saveJSON("library.json", a.lib)
		}
		a.mu.Unlock()
	}
}

func (a *App) artLoop() {
	time.Sleep(3 * time.Second)
	for {
		a.artPass("")
		select {
		case k := <-a.artKick:
			if k != "" {
				artTriedMu.Lock()
				delete(artTried, k)
				artTriedMu.Unlock()
				a.artPass(k)
			}
		case <-time.After(30 * time.Second):
		}
	}
}
