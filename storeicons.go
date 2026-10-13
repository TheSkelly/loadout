package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// When the PS5 has no icon for a game (common for PS4 games), Loadout asks
// PlayStation's public store catalog for the game's official artwork, once.

var (
	storeTriedMu sync.Mutex
	storeTried   = map[string]bool{}
)

var storeBase = "https://store.playstation.com"

var storeRegions = []string{"US/en", "GB/en", "CA/en", "AU/en", "JP/ja", "HK/en"}

type storeImage struct {
	Type int    `json:"type"`
	URL  string `json:"url"`
}

// storeIconURL looks a title ID up in the store catalog and returns the best image.
func storeIconURL(cl *http.Client, id string) string {
	for _, reg := range storeRegions {
		u := storeBase + "/store/api/chihiro/00_09_000/titlecontainer/" + reg + "/999/" + id + "_00"
		resp, err := cl.Get(u)
		if err != nil {
			return "" // offline: don't keep trying regions
		}
		var d struct {
			Images []storeImage `json:"images"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&d)
		resp.Body.Close()
		if err != nil || len(d.Images) == 0 {
			continue
		}
		// type 1 is the square icon; 10 and 2 are the next best square-ish art
		rank := map[int]int{1: 0, 10: 1, 2: 2, 12: 3}
		sort.SliceStable(d.Images, func(i, j int) bool {
			ri, ok := rank[d.Images[i].Type]
			if !ok {
				ri = 9
			}
			rj, ok := rank[d.Images[j].Type]
			if !ok {
				rj = 9
			}
			return ri < rj
		})
		for _, im := range d.Images {
			if strings.HasPrefix(im.URL, "https://") {
				return im.URL
			}
		}
	}
	return ""
}

// fetchStoreIcons fills in missing icons for these title IDs from the store.
// Returns true if any new icon was saved.
func fetchStoreIcons(ids []string) bool {
	return fetchStoreIconsWith(&http.Client{Timeout: 15 * time.Second}, ids)
}

func fetchStoreIconsWith(cl *http.Client, ids []string) bool {
	got := false
	for _, id := range ids {
		storeTriedMu.Lock()
		tried := storeTried[id]
		storeTried[id] = true
		storeTriedMu.Unlock()
		if tried || fileExists(ps5IconPath(id)) {
			continue
		}
		u := storeIconURL(cl, id)
		if u == "" {
			continue
		}
		resp, err := cl.Get(u)
		if err != nil {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		ct := http.DetectContentType(b)
		if err != nil || resp.StatusCode != 200 || !strings.HasPrefix(ct, "image/") {
			continue
		}
		dst := ps5IconPath(id)
		os.MkdirAll(filepath.Dir(dst), 0755)
		if os.WriteFile(dst+".tmp", b, 0644) == nil && os.Rename(dst+".tmp", dst) == nil {
			got = true
		}
	}
	return got
}
