package main

import (
	"archive/zip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// The homebrew store: the homebrew.page catalog, the one ProsperoStore
// installs from (https://github.com/blackbearreloaded/ProsperoStore), read
// through its store API (ps5-homebrew-catalog docs/api.md). The PC does the
// work: it checks the catalog's Ed25519 signature and every file's SHA-256,
// downloads the app, checks it against the signed hash, unpacks it and puts
// the app folder in /data/homebrew, where ShadowMountPlus registers it.

var hbBases = []string{
	"https://homebrew.page/api/v1/",
	"https://blackbearreloaded.github.io/ps5-homebrew-catalog/api/v1/", // the mirror, only if the site fails
}

// the catalog's two signing keys; either may sign
var hbKeys = []string{
	"87391bf1698ecef101bf5e29dc8585ee5947d571e19470de7411c5d3b137b5cf",
	"509bcfab7edfb4e5ed23639488517c6ef2657c13b6b7f2bf699c8989d9b0dd7b",
}

const (
	hbStage    = "/data/loadout/hb-next"     // an app goes up here first, then is renamed into place
	hbPrevious = "/data/loadout/hb-previous" // the copy an update replaced, kept until the next one
)

type hbApp struct {
	TitleID        string  `json:"titleid"`
	Status         string  `json:"status"`
	Name           string  `json:"name"`
	Kind           string  `json:"kind"`
	Author         string  `json:"author"`
	Description    string  `json:"description,omitempty"`
	License        string  `json:"license,omitempty"`
	Version        string  `json:"version"`
	ContentVersion *string `json:"content_version"`
	Format         *string `json:"format"`
	ArtifactURL    string  `json:"artifact_url,omitempty"`
	ArtifactName   string  `json:"artifact_name,omitempty"`
	SHA256         string  `json:"sha256,omitempty"`
	Size           *int64  `json:"size"`
	Released       *string `json:"released"`
	Updated        *string `json:"updated"`
	Added          *string `json:"added,omitempty"`
	IconSmall      *string `json:"icon_small"`
	Icon           *string `json:"icon,omitempty"`
	Page           string  `json:"page,omitempty"`
	ReleaseNotes   *string `json:"release_notes,omitempty"`
	Sandbox        *string `json:"sandbox,omitempty"`
	Safety         *struct {
		Sandbox string `json:"sandbox"`
		Network bool   `json:"network"`
		Build   string `json:"build"`
	} `json:"safety,omitempty"`
}

// hbView is one app as Loadout's screens show it.
type hbView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Author      string `json:"author"`
	Version     string `json:"version"`
	Content     string `json:"content"` // the catalog's content_version
	Format      string `json:"format"`
	Size        int64  `json:"size"`
	Released    string `json:"released"`
	Added       string `json:"added"`
	Icon        string `json:"icon"`
	Sandbox     string `json:"sandbox"` // stays, leaves, unclear or ""
	Status      string `json:"status"`  // available, coming_soon
	Installable bool   `json:"installable"`
	Installed   string `json:"installed"` // contentVersion on the PS5, "" when not installed
	Where       string `json:"where"`     // its folder on the PS5
	Update      bool   `json:"update"`
}

type hbManifest struct {
	Files    map[string]string `json:"files"`
	Sequence int64             `json:"sequence"`
}

var hbCat struct {
	sync.Mutex
	at       time.Time
	base     string
	manifest hbManifest
	index    []hbApp
	err      string
}

func hbGet(url string, max int64) ([]byte, error) {
	cl := &http.Client{Timeout: 30 * time.Second}
	resp, err := cl.Get(url)
	if err != nil {
		return nil, errors.New(shortNetErr(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.New(resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, max))
}

// hbVerifiedManifest fetches and checks the manifest from one base.
func hbVerifiedManifest(base string) (hbManifest, error) {
	var m hbManifest
	body, err := hbGet(base+"manifest.json", 4<<20)
	if err != nil {
		return m, err
	}
	sig, err := hbGet(base+"manifest.sig", 1024)
	if err != nil {
		return m, err
	}
	ok := false
	for _, k := range hbKeys {
		pub, _ := hex.DecodeString(k)
		if len(sig) == ed25519.SignatureSize && ed25519.Verify(ed25519.PublicKey(pub), body, sig) {
			ok = true
		}
	}
	if !ok {
		return m, errors.New("the catalog's signature doesn't check out")
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return m, errors.New("the catalog's manifest can't be read")
	}
	// the sequence never goes down: an older catalog is a replayed one
	var seen struct{ Sequence int64 }
	loadJSON("homebrew-catalog.json", &seen)
	if m.Sequence < seen.Sequence {
		return m, errors.New("the catalog is older than one Loadout already saw")
	}
	if m.Sequence > seen.Sequence {
		seen.Sequence = m.Sequence
		saveJSON("homebrew-catalog.json", seen)
	}
	return m, nil
}

// hbFile fetches one API file and checks it against the manifest.
func hbFile(base string, m hbManifest, name string) ([]byte, error) {
	want, ok := m.Files[name]
	if !ok {
		return nil, fmt.Errorf("%s isn't in the catalog", name)
	}
	b, err := hbGet(base+name, 16<<20)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), want) {
		return nil, fmt.Errorf("%s doesn't match the catalog", name)
	}
	return b, nil
}

// hbLoad reads the catalog, from the site or its mirror, at most every 10 minutes.
func hbLoad(refresh bool) (string, hbManifest, []hbApp, error) {
	hbCat.Lock()
	defer hbCat.Unlock()
	if !refresh && hbCat.index != nil && time.Since(hbCat.at) < 10*time.Minute {
		return hbCat.base, hbCat.manifest, hbCat.index, nil
	}
	var lastErr error
	for _, base := range hbBases {
		m, err := hbVerifiedManifest(base)
		if err != nil {
			lastErr = err
			continue
		}
		b, err := hbFile(base, m, "index.json")
		if err != nil {
			lastErr = err
			continue
		}
		var idx struct {
			Apps []hbApp `json:"apps"`
		}
		if err := json.Unmarshal(b, &idx); err != nil {
			lastErr = err
			continue
		}
		hbCat.at, hbCat.base, hbCat.manifest, hbCat.index, hbCat.err = time.Now(), base, m, idx.Apps, ""
		return base, m, idx.Apps, nil
	}
	if hbCat.index != nil {
		return hbCat.base, hbCat.manifest, hbCat.index, nil // keep showing the last good one
	}
	return "", hbManifest{}, nil, fmt.Errorf("Couldn't read the homebrew catalog: %v", lastErr)
}

// hbAppFile is one app's full, checked record.
func hbAppFile(id string) (hbApp, error) {
	var a hbApp
	base, m, _, err := hbLoad(false)
	if err != nil {
		return a, err
	}
	b, err := hbFile(base, m, "apps/"+id+".json")
	if err != nil {
		return a, err
	}
	if err := json.Unmarshal(b, &a); err != nil {
		return a, err
	}
	if a.Status != "available" || a.ArtifactURL == "" || a.SHA256 == "" {
		return a, errors.New("That app can't be installed yet.")
	}
	return a, nil
}

var cvRe = regexp.MustCompile(`^\d{2}\.\d{3}\.\d{3}$`)

// newerContent: catalog's content version is higher than the installed one
func newerContent(cat, inst string) bool {
	return cvRe.MatchString(cat) && cvRe.MatchString(inst) && cat > inst
}

// hbInstalled finds apps on the PS5: folders in the homebrew folders with a
// param.json, by title ID, with their contentVersion.
func (a *App) hbInstalled() map[string][2]string {
	out := map[string][2]string{}
	a.mu.Lock()
	host, port := strings.TrimSpace(a.cfg.PS5IP), a.cfg.PS5Port
	roots := a.fxRootsLocked("homebrew")
	a.mu.Unlock()
	if host == "" {
		return out
	}
	f, err := ftpDial(host, port)
	if err != nil {
		return out
	}
	defer f.Close()
	for _, r := range roots {
		if !strings.HasSuffix(r.Path, "/homebrew") {
			continue
		}
		ents, err := f.list(r.Path)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !e.IsDir || strings.HasPrefix(e.Name, ".") {
				continue
			}
			dir := path.Join(r.Path, e.Name)
			b, err := f.get(path.Join(dir, "sce_sys", "param.json"), 1<<20)
			if err != nil {
				continue
			}
			var pj struct {
				TitleID        string `json:"titleId"`
				ContentVersion string `json:"contentVersion"`
			}
			if json.Unmarshal(b, &pj) != nil || pj.TitleID == "" {
				continue
			}
			out[strings.ToUpper(pj.TitleID)] = [2]string{pj.ContentVersion, dir}
		}
	}
	return out
}

func strOr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// homebrewList is the catalog with what this PS5 has.
func (a *App) homebrewList(refresh bool) ([]hbView, error) {
	_, _, apps, err := hbLoad(refresh)
	if err != nil {
		return nil, err
	}
	inst := a.hbInstalled()
	out := make([]hbView, 0, len(apps))
	for _, x := range apps {
		v := hbView{ID: x.TitleID, Name: x.Name, Author: x.Author, Version: x.Version, Content: strOr(x.ContentVersion), Format: strOr(x.Format),
			Released: strOr(x.Released), Added: strOr(x.Added), Icon: strOr(x.IconSmall), Sandbox: strOr(x.Sandbox), Status: x.Status}
		if x.Size != nil {
			v.Size = *x.Size
		}
		v.Installable = x.Status == "available" && hbFormatOK(v.Format)
		if i, ok := inst[strings.ToUpper(x.TitleID)]; ok {
			v.Installed, v.Where = i[0], i[1]
			if v.Installed == "" {
				v.Installed = "installed"
			}
			v.Update = newerContent(v.Content, i[0])
		}
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

func hbFormatOK(f string) bool { return f == "zip" || f == "ffpkg" || f == "ffpfsc" }

// homebrewDetail is one app with its description and release notes.
func (a *App) homebrewDetail(id string) (map[string]any, error) {
	x, err := hbAppFile(id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": x.TitleID, "name": x.Name, "author": x.Author, "description": x.Description, "license": x.License,
		"version": x.Version, "notes": strOr(x.ReleaseNotes), "page": x.Page, "icon": strOr(x.Icon), "safety": x.Safety}, nil
}

// startHomebrew queues an install (or update) or an uninstall in the PS5 lane.
func (a *App) startHomebrew(id, action string) (string, error) {
	id = strings.ToUpper(strings.TrimSpace(id))
	if !regexp.MustCompile(`^[A-Z]{4}\d{5}$`).MatchString(id) {
		return "", errors.New("That isn't a title ID.")
	}
	if id == ps5AppID {
		return "", errors.New("Loadout's own app is updated by Loadout.")
	}
	name := id
	_, _, apps, _ := hbLoad(false)
	for _, x := range apps {
		if x.TitleID == id {
			name = x.Name
		}
	}
	kind, label := "hbinst", "Installing "+name
	if action == "uninstall" {
		kind, label = "hbdel", "Removing "+name
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, j := range a.build.Jobs {
		if j.Key == "hb:"+id && (j.State == "queued" || j.State == "sending" || j.State == "deleting") {
			return "", errors.New("That app is already on its way.")
		}
	}
	j := &BuildJob{Key: "hb:" + id, Kind: kind, Name: name, Out: id, State: "queued"}
	a.addJobsLocked([]*BuildJob{j})
	a.enqueuePS5Locked(j)
	return label + ".", nil
}

// runHomebrewInstall: download, check, unpack, upload beside the old copy,
// then swap it in.
func (a *App) runHomebrewInstall(j *BuildJob) (string, error) {
	id := j.Out
	cancelled := func() bool { return a.ps5Stopped(j) }
	setNote := func(state, note string, pct int) {
		a.mu.Lock()
		j.State, j.Note, j.Pct = state, note, pct
		a.mu.Unlock()
	}
	setNote("sending", "Reading the catalog", 0)
	x, err := hbAppFile(id)
	if err != nil {
		return "", err
	}
	format := strOr(x.Format)
	if !hbFormatOK(format) {
		return "", fmt.Errorf("Loadout can't install a %s app.", format)
	}
	tmp, err := os.MkdirTemp("", "loadout-hb-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	file := filepath.Join(tmp, "download")
	setNote("sending", "Downloading", 0)
	if err := hbDownload(x.ArtifactURL, file, x.SHA256, func(done, total int64) {
		if total > 0 {
			a.mu.Lock()
			j.Pct = int(done * 400 / total) // the download is the first 40%
			a.mu.Unlock()
		}
	}, cancelled); err != nil {
		return "", err
	}

	a.mu.Lock()
	host, port := strings.TrimSpace(a.cfg.PS5IP), a.cfg.PS5Port
	a.mu.Unlock()
	f, err := ftpDial(host, port)
	if err != nil {
		return "", errors.New("Loadout can't reach the PS5's FTP server.")
	}
	defer f.Close()
	inst := a.hbInstalled()
	old, had := inst[id]
	prog := func(done, total int64) {
		if total > 0 {
			a.mu.Lock()
			j.Pct = 400 + int(done*600/total)
			a.mu.Unlock()
		}
	}

	if format != "zip" {
		// an image: ShadowMountPlus mounts the file as it is
		name := id + "." + format
		setNote("sending", "Copying to the PS5", 400)
		if _, err := f.uploadAs(file, "/data/homebrew", name, true, prog, cancelled); err != nil {
			return "", err
		}
		return fmt.Sprintf("Installed %s %s. ShadowMountPlus adds it to the home screen.", x.Name, x.Version), nil
	}

	setNote("sending", "Unpacking", 400)
	dir := filepath.Join(tmp, "app")
	root, err := hbUnzip(file, dir, id)
	if err != nil {
		return "", err
	}
	stage := path.Join(hbStage, id)
	if _, err := f.list(stage); err == nil {
		f.removeAll(stage, true, func() {}, func() bool { return false })
	}
	if err := f.mkdirAll(hbStage); err != nil {
		return "", err
	}
	setNote("sending", "Copying to the PS5", 400)
	if _, err := uploadTreeFiles(f, root, stage, prog, cancelled); err != nil {
		return "", err
	}
	setNote("sending", "Putting it in place", 1000)
	dest := path.Join("/data/homebrew", id)
	if had {
		dest = old[1] // an update goes where the app already is
		prev := path.Join(hbPrevious, id)
		if _, err := f.list(prev); err == nil {
			f.removeAll(prev, true, func() {}, func() bool { return false })
		}
		f.mkdirAll(hbPrevious)
		if err := f.rename(dest, prev); err != nil {
			return "", fmt.Errorf("Couldn't move the old copy aside (close %s on the PS5 first): %v", x.Name, err)
		}
	}
	if err := f.rename(stage, dest); err != nil {
		if had {
			f.rename(path.Join(hbPrevious, id), dest)
		}
		return "", err
	}
	if had {
		return fmt.Sprintf("Updated %s to %s. The old copy is kept in %s until the next update.", x.Name, x.Version, hbPrevious), nil
	}
	return fmt.Sprintf("Installed %s %s. ShadowMountPlus adds it to the home screen.", x.Name, x.Version), nil
}

// runHomebrewRemove deletes an app's folder. Its data folder is left.
func (a *App) runHomebrewRemove(j *BuildJob) (string, error) {
	inst := a.hbInstalled()
	old, ok := inst[j.Out]
	if !ok {
		return "", errors.New("It isn't installed.")
	}
	a.mu.Lock()
	j.State, j.Note = "deleting", "Removing"
	host, port := strings.TrimSpace(a.cfg.PS5IP), a.cfg.PS5Port
	a.mu.Unlock()
	f, err := ftpDial(host, port)
	if err != nil {
		return "", errors.New("Loadout can't reach the PS5's FTP server.")
	}
	defer f.Close()
	if err := f.removeAll(old[1], true, func() {}, func() bool { return false }); err != nil {
		return "", err
	}
	return "Removed. ShadowMountPlus takes it off the home screen.", nil
}

func hbDownload(url, dst, want string, prog func(done, total int64), cancelled func() bool) error {
	cl := &http.Client{Timeout: 30 * time.Minute}
	resp, err := cl.Get(url)
	if err != nil {
		return fmt.Errorf("Couldn't download it: %s", shortNetErr(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("Couldn't download it (%s).", resp.Status)
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	h := sha256.New()
	buf := make([]byte, 1<<20)
	var done int64
	for {
		if cancelled() {
			return ErrCancelled
		}
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			out.Write(buf[:n])
			h.Write(buf[:n])
			done += int64(n)
			prog(done, resp.ContentLength)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("The download was cut off: %s", shortNetErr(rerr))
		}
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), want) {
		return errors.New("The download doesn't match the catalog's checksum, so Loadout didn't install it.")
	}
	return nil
}

// hbUnzip extracts the archive and returns the app folder: <TITLEID>/ at the
// top, or the top itself when eboot.bin is there.
func hbUnzip(file, dir, id string) (string, error) {
	zr, err := zip.OpenReader(file)
	if err != nil {
		return "", errors.New("The download isn't a ZIP.")
	}
	defer zr.Close()
	for _, zf := range zr.File {
		name := filepath.FromSlash(zf.Name)
		p := filepath.Join(dir, name)
		if !strings.HasPrefix(p, filepath.Clean(dir)+string(os.PathSeparator)) {
			return "", errors.New("The ZIP has a path that points outside it.")
		}
		if zf.FileInfo().IsDir() {
			os.MkdirAll(p, 0755)
			continue
		}
		os.MkdirAll(filepath.Dir(p), 0755)
		r, err := zf.Open()
		if err != nil {
			return "", err
		}
		w, err := os.Create(p)
		if err != nil {
			r.Close()
			return "", err
		}
		_, err = io.Copy(w, r)
		r.Close()
		w.Close()
		if err != nil {
			return "", err
		}
	}
	for _, c := range []string{filepath.Join(dir, id), dir} {
		if fileExists(filepath.Join(c, "eboot.bin")) && fileExists(filepath.Join(c, "sce_sys", "param.json")) {
			return c, nil
		}
	}
	// one folder at the top with another name
	if ents, _ := os.ReadDir(dir); len(ents) == 1 && ents[0].IsDir() {
		c := filepath.Join(dir, ents[0].Name())
		if fileExists(filepath.Join(c, "eboot.bin")) {
			return c, nil
		}
	}
	return "", errors.New("The ZIP doesn't hold an app folder (eboot.bin and sce_sys).")
}
