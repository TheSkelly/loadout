package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Homebrew apps whose own GitHub releases are newer than the catalog's
// listing. ProsperoEden puts out a release almost every day; homebrew.page
// picks them up later. Loadout reads the repository's releases and, when the
// newest is ahead of the catalog, installs that one instead: the ZIP of the
// app folder, checked against the release's SHA256SUMS and the digest GitHub
// keeps for the file.

type ghSource struct{ ID, Name, Repo string }

var ghSources = []ghSource{
	{ID: "PPSA99008", Name: "ProsperoEden", Repo: "Swordpdf/ProsperoEden"},
}

type hbRelease struct {
	Tag, Content, URL, SHA, Released, Notes, Page string
	Size                                          int64
	Pre                                           bool
}

var hbRelCache struct {
	sync.Mutex
	at  map[string]time.Time
	rel map[string]hbRelease
	err map[string]error
}

// tagContent: "v1.005.000" -> "01.005.000", the form param.json uses.
var tagNums = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

func tagContent(tag string) string {
	m := tagNums.FindStringSubmatch(tag)
	if m == nil {
		return ""
	}
	// decimal even with leading zeros ("010" is ten, not octal eight)
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[2])
	c, _ := strconv.Atoi(m[3])
	return fmt.Sprintf("%02d.%03d.%03d", a, b, c)
}

func ghSourceFor(id string) (ghSource, bool) {
	for _, s := range ghSources {
		if strings.EqualFold(s.ID, id) {
			return s, true
		}
	}
	return ghSource{}, false
}

// ghLatest is a repository's newest release with an app ZIP (pre-releases
// count: ProsperoEden only makes those), at most every 10 minutes.
func ghLatest(repo string, refresh bool) (hbRelease, error) {
	hbRelCache.Lock()
	defer hbRelCache.Unlock()
	if hbRelCache.at == nil {
		hbRelCache.at, hbRelCache.rel, hbRelCache.err = map[string]time.Time{}, map[string]hbRelease{}, map[string]error{}
	}
	if !refresh && time.Since(hbRelCache.at[repo]) < 10*time.Minute {
		return hbRelCache.rel[repo], hbRelCache.err[repo]
	}
	r, err := ghFetch(repo)
	hbRelCache.at[repo], hbRelCache.rel[repo], hbRelCache.err[repo] = time.Now(), r, err
	return r, err
}

func ghFetch(repo string) (hbRelease, error) {
	var out hbRelease
	cl := &http.Client{Timeout: 20 * time.Second}
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/"+repo+"/releases?per_page=10", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := cl.Do(req)
	if err != nil {
		return out, fmt.Errorf("GitHub: %s", shortNetErr(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return out, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var rels []struct {
		Tag       string `json:"tag_name"`
		Draft     bool   `json:"draft"`
		Pre       bool   `json:"prerelease"`
		Published string `json:"published_at"`
		Body      string `json:"body"`
		Page      string `json:"html_url"`
		Assets    []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Size   int64  `json:"size"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rels); err != nil {
		return out, errors.New("GitHub's answer can't be read")
	}
	for _, r := range rels {
		if r.Draft {
			continue
		}
		var zip, sums string
		var size int64
		var digest string
		for _, a := range r.Assets {
			n := strings.ToLower(a.Name)
			switch {
			case strings.HasSuffix(n, ".zip") && zip == "":
				zip, size, digest = a.URL, a.Size, strings.TrimPrefix(a.Digest, "sha256:")
			case n == "sha256sums" || n == "sha256sums.txt":
				sums = a.URL
			}
		}
		if zip == "" {
			continue
		}
		sha := strings.ToLower(digest)
		// the release's own checksum file, which must agree with GitHub's digest
		if sums != "" {
			if b, err := hbGet(sums, 64<<10); err == nil {
				for _, line := range strings.Split(string(b), "\n") {
					f := strings.Fields(line)
					if len(f) == 2 && strings.EqualFold(strings.TrimPrefix(f[1], "*"), zip[strings.LastIndex(zip, "/")+1:]) {
						if sha != "" && !strings.EqualFold(sha, f[0]) {
							return out, fmt.Errorf("%s's checksum file and GitHub disagree about the download", r.Tag)
						}
						sha = strings.ToLower(f[0])
					}
				}
			}
		}
		if len(sha) != 64 {
			return out, fmt.Errorf("%s has no checksum to check the download against", r.Tag)
		}
		return hbRelease{Tag: r.Tag, Content: tagContent(r.Tag), URL: zip, SHA: sha, Size: size, Released: r.Published,
			Notes: r.Body, Page: r.Page, Pre: r.Pre}, nil
	}
	return out, errors.New("no release with an app ZIP")
}

// ghNewer is the GitHub release to use for an app instead of the catalog's
// listing (with content version have), if there's a newer one.
func ghNewer(id, have string, refresh bool) (hbRelease, bool) {
	s, ok := ghSourceFor(id)
	if !ok {
		return hbRelease{}, false
	}
	r, err := ghLatest(s.Repo, refresh)
	if err != nil || r.Content == "" {
		return hbRelease{}, false
	}
	if have != "" && !newerContent(r.Content, have) {
		return hbRelease{}, false
	}
	return r, true
}

// hbUpdateFor is the newest release of an installed app (its GitHub
// releases or the catalog, whichever is ahead) when it's newer than have,
// or "".
func hbUpdateFor(id, have string) string {
	if have == "" {
		return ""
	}
	best, bestContent := "", have
	if r, ok := ghNewer(id, have, false); ok {
		best, bestContent = r.Tag, r.Content
	}
	if _, _, apps, err := hbLoad(false); err == nil {
		for _, x := range apps {
			if strings.EqualFold(x.TitleID, id) && x.Status == "available" && newerContent(strOr(x.ContentVersion), bestContent) {
				best, bestContent = x.Version, strOr(x.ContentVersion)
			}
		}
	}
	return best
}
