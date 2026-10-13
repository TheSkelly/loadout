package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// A plain English description for every game, from Wikipedia. Store text can
// be missing, in another language, or mostly a sales pitch; this is the same
// kind of short summary for every game.

func wikiBase() string {
	if b := os.Getenv("TT_WIKIBASE"); b != "" { // test stand-in
		return b
	}
	return "https://en.wikipedia.org"
}

func wikiGet(u string, v any) error {
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("User-Agent", "Loadout/"+appVersion+" (game library app)")
	req.Header.Set("Accept", "application/json")
	resp, err := storeHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return errNoPage
	}
	if resp.StatusCode != 200 {
		return errors.New("wikipedia: " + resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

var errNoPage = errors.New("no page")
var nonWord = regexp.MustCompile(`[^a-z0-9]+`)

// nameWords are the words of a game's name that a matching article must have.
func nameWords(name string) []string {
	name = strings.NewReplacer("™", "", "®", "", "©", "", "Δ", " delta ", "ö", "o", "ō", "o", "é", "e").Replace(strings.ToLower(name))
	skip := map[string]bool{"the": true, "of": true, "a": true, "an": true, "and": true, "edition": true, "remastered": true, "remake": true,
		"special": true, "deluxe": true, "standard": true, "ps5": true, "ps4": true, "hd": true, "2d": true, "i": true}
	var out []string
	for _, w := range nonWord.Split(name, -1) {
		if w != "" && !skip[w] {
			out = append(out, w)
		}
	}
	return out
}

func wikiMatches(name, title, extract string) bool {
	lead := extract
	if len(lead) > 300 {
		lead = lead[:300]
	}
	hay := " " + strings.Join(nameWords(title+" "+lead), " ") + " "
	if !strings.Contains(strings.ToLower(extract), "game") {
		return false
	}
	words := nameWords(name)
	if len(words) == 0 {
		return false
	}
	hit := 0
	for _, w := range words {
		if strings.Contains(hay, " "+w+" ") {
			hit++
		}
	}
	return hit*10 >= len(words)*7 // most of the name, allowing a subtitle the article words differently
}

// trimAbout keeps a summary to a few sentences.
func trimAbout(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	if i := strings.LastIndex(cut, ". "); i > max/3 {
		return cut[:i+1]
	}
	return strings.TrimSpace(cut[:strings.LastIndex(cut, " ")]) + "…"
}

// wikiAbout finds the game's article and returns its summary and link.
// found is false with a nil error when Wikipedia answered but has no match.
func wikiAbout(name string) (about, link string, err error) {
	clean := strings.TrimSpace(strings.NewReplacer("™", "", "®", "", "©", "").Replace(name))
	if clean == "" {
		return "", "", nil
	}
	var sr struct {
		Query struct {
			Search []struct{ Title string }
		}
	}
	q := url.Values{"action": {"query"}, "format": {"json"}, "list": {"search"}, "srlimit": {"5"}, "srsearch": {clean + " video game"}}
	if err := wikiGet(wikiBase()+"/w/api.php?"+q.Encode(), &sr); err != nil {
		return "", "", err
	}
	for _, hit := range sr.Query.Search {
		var sum struct {
			Type        string
			Title       string
			Extract     string
			ContentURLs struct {
				Desktop struct{ Page string }
			} `json:"content_urls"`
		}
		err := wikiGet(wikiBase()+"/api/rest_v1/page/summary/"+url.PathEscape(strings.ReplaceAll(hit.Title, " ", "_")), &sum)
		if errors.Is(err, errNoPage) {
			continue
		}
		if err != nil {
			return "", "", err
		}
		if sum.Type != "standard" || !wikiMatches(clean, sum.Title, sum.Extract) {
			continue
		}
		return trimAbout(sum.Extract, 700), sum.ContentURLs.Desktop.Page, nil
	}
	return "", "", nil
}

var jpGenres = map[string]string{
	"アクション": "Action", "ホラー": "Horror", "アドベンチャー": "Adventure", "ロールプレイング": "Role Playing", "RPG": "RPG",
	"シューティング": "Shooter", "スポーツ": "Sports", "レース": "Racing", "レーシング": "Racing", "格闘": "Fighting", "シミュレーション": "Simulation",
	"パズル": "Puzzle", "ストラテジー": "Strategy", "音楽/リズム": "Music", "ファミリー": "Family", "カジュアル": "Casual", "ユニーク": "Unique",
	"アーケード": "Arcade", "パーティー": "Party", "教育": "Educational",
}

// enGenres shows store genres in English when the store page was Japanese.
func enGenres(gs []string) []string {
	out := make([]string, len(gs))
	for i, g := range gs {
		if en, ok := jpGenres[strings.TrimSpace(g)]; ok {
			g = en
		}
		out[i] = g
	}
	return out
}
