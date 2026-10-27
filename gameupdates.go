package main

// Newer-version check for games. Each PS5 game's param.json carries
// versionFileUri, Sony's public "what is the latest patch" file for that title.
// We read the newest app version from it and compare with the contentVersion
// of the copy in the library. Nothing is downloaded: this only tells you a
// newer version exists so you know to look for an updated dump.

import (
	"crypto/tls"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	updateTTL      = 6 * time.Hour
	updateFailTTL  = 30 * time.Minute
	updateMaxBytes = 1 << 20
)

type updateInfo struct {
	Latest  string
	Checked time.Time
	Failed  bool
}

var (
	updateMu    sync.Mutex
	updateCache = map[string]updateInfo{} // by title ID
)

// versionURIAllowed: only Sony's download hosts over https, because the
// request skips certificate checks (see versionClient).
func versionURIAllowed(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return strings.HasSuffix(h, ".dl.playstation.net")
}

// parseLatestAppVersion returns the newest game (not DLC) version in Sony's
// version.xml.
func parseLatestAppVersion(b []byte) (string, error) {
	var doc struct {
		Apps []struct {
			Packages []struct {
				Ver string `xml:"content_ver,attr"`
			} `xml:"package"`
		} `xml:"app_tag"`
	}
	if err := xml.Unmarshal(b, &doc); err != nil {
		return "", err
	}
	best := ""
	for _, app := range doc.Apps {
		for _, p := range app.Packages {
			v := strings.TrimSpace(p.Ver)
			if v != "" && (best == "" || versionNewer(v, best)) {
				best = v
			}
		}
	}
	if best == "" {
		return "", errors.New("the file lists no game version")
	}
	return best, nil
}

// versionNewer: "01.300.000" is newer than "01.200.000". Anything that isn't
// dotted numbers, or an empty side, is never newer.
func versionNewer(latest, have string) bool {
	l, ok1 := versionParts(latest)
	h, ok2 := versionParts(have)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < len(l) || i < len(h); i++ {
		var a, b int
		if i < len(l) {
			a = l[i]
		}
		if i < len(h) {
			b = h[i]
		}
		if a != b {
			return a > b
		}
	}
	return false
}

func versionParts(s string) ([]int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, false
	}
	var out []int
	for _, p := range strings.Split(s, ".") {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

// versionClient skips certificate checks: Sony's download hosts chain to a
// root Windows doesn't trust. That is acceptable only because versionURIAllowed
// limits it to Sony's hosts and the answer is just a version label.
var versionClient = &http.Client{
	Timeout:   15 * time.Second,
	Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if !versionURIAllowed(req.URL.String()) {
			return errors.New("redirected away from Sony's servers")
		}
		return nil
	},
}

func fetchLatestVersion(uri string) (string, error) {
	uri = strings.TrimSpace(uri)
	if !versionURIAllowed(uri) {
		return "", errors.New("not a Sony version file address")
	}
	resp, err := versionClient.Get(uri)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errors.New("Sony's server answered " + resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, updateMaxBytes))
	if err != nil {
		return "", err
	}
	return parseLatestAppVersion(b)
}

// latestKnown is the cached newest version for a title ("" when unknown).
func latestKnown(titleID string) string {
	updateMu.Lock()
	defer updateMu.Unlock()
	return updateCache[titleID].Latest
}

func updateDue(titleID string, now time.Time) bool {
	updateMu.Lock()
	defer updateMu.Unlock()
	c, ok := updateCache[titleID]
	if !ok {
		return true
	}
	ttl := updateTTL
	if c.Failed {
		ttl = updateFailTTL
	}
	return now.Sub(c.Checked) > ttl
}

func storeLatest(titleID, latest string, failed bool) {
	updateMu.Lock()
	defer updateMu.Unlock()
	prev := updateCache[titleID]
	if failed {
		latest = prev.Latest // keep what we knew
	}
	updateCache[titleID] = updateInfo{Latest: latest, Checked: time.Now(), Failed: failed}
}

// updateLoop checks every library game that has a version file address, a few
// at a time, then waits and repeats.
func (a *App) updateLoop() {
	time.Sleep(20 * time.Second)
	for {
		a.checkGameUpdates()
		time.Sleep(30 * time.Minute)
	}
}

func (a *App) checkGameUpdates() {
	a.mu.Lock()
	todo := map[string]string{}
	for _, e := range a.lib.Games {
		if e.TitleID != "" && e.VersionURI != "" && updateDue(e.TitleID, time.Now()) {
			todo[e.TitleID] = e.VersionURI
		}
	}
	a.mu.Unlock()
	for id, uri := range todo {
		v, err := fetchLatestVersion(uri)
		storeLatest(id, v, err != nil)
		time.Sleep(300 * time.Millisecond)
	}
}
