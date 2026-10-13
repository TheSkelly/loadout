package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

// User profiles on the PS5: names and pictures. This goes through the
// PS5Upload helper (the same one used for sending games); Loadout only asks
// the bundled PS5Upload engine to do it.

type profUser struct {
	UID     uint32 `json:"uid"`
	Name    string `json:"name"`
	Picture string `json:"picture"` // the current picture as a data: address, if the PS5 has one
}

func (a *App) profReady() (base, host string, err error) {
	host, _, _ = a.ps5()
	if host == "" {
		return "", "", errors.New("Set your PS5's IP address in Settings first.")
	}
	if !p5uAvailable() {
		return "", "", errors.New("Profiles need the PS5Upload engine, which isn't installed with this copy of Loadout. Reinstall Loadout to get it.")
	}
	p5uHelp.Lock()
	delete(p5uBad, host) // asked for by hand: try the helper again now
	p5uHelp.Unlock()
	base, err = p5uReady(host, nil)
	if err != nil {
		return "", "", fmt.Errorf("Loadout couldn't start PS5Upload's helper on the PS5: %v", err)
	}
	return base, host, nil
}

func (a *App) profList() ([]profUser, error) {
	base, host, err := a.profReady()
	if err != nil {
		return nil, err
	}
	var info struct {
		Users []struct {
			UID      uint32 `json:"uid"`
			Username string `json:"username"`
		} `json:"users"`
	}
	if err := p5uCall("GET", base+"/api/profile/info?addr="+url.QueryEscape(host), nil, &info); err != nil {
		return nil, fmt.Errorf("the PS5 didn't give its list of users: %v", err)
	}
	out := []profUser{}
	for _, u := range info.Users {
		pu := profUser{UID: u.UID, Name: u.Username}
		var cur struct {
			DataURL *string `json:"data_url"`
		}
		if p5uCall("GET", fmt.Sprintf("%s/api/profile/avatar/current?addr=%s&uid=%d", base, url.QueryEscape(host), u.UID), nil, &cur) == nil && cur.DataURL != nil {
			pu.Picture = *cur.DataURL
		}
		out = append(out, pu)
	}
	return out, nil
}

func profMode(m string) string {
	if m == "fit" {
		return "fit"
	}
	return "crop"
}

// profPreview shows how a picture will look once squared, without touching the PS5.
func profPreview(path, mode string) (string, error) {
	base, err := p5uStart()
	if err != nil {
		return "", err
	}
	var res struct {
		DataURL string `json:"data_url"`
	}
	if err := p5uCall("POST", base+"/api/profile/avatar/preview", map[string]string{"image_path": path, "mode": profMode(mode)}, &res); err != nil {
		return "", fmt.Errorf("that picture couldn't be read (%v). Try a PNG or JPG", err)
	}
	return res.DataURL, nil
}

func (a *App) profRoutes(api func(string, func(r *http.Request) (any, error)), body func(*http.Request, any)) {
	api("/api/prof/list", func(r *http.Request) (any, error) {
		return a.profList()
	})
	api("/api/prof/rename", func(r *http.Request) (any, error) {
		var b struct {
			UID  uint32
			Name string
		}
		body(r, &b)
		name := strings.TrimSpace(b.Name)
		if name == "" || utf8.RuneCountInString(name) > 16 {
			return nil, errors.New("A profile name needs 1 to 16 characters.")
		}
		for _, c := range name {
			if c < 0x20 {
				return nil, errors.New("That name has a character the PS5 won't accept.")
			}
		}
		base, host, err := a.profReady()
		if err != nil {
			return nil, err
		}
		if err := p5uCall("POST", base+"/api/profile/local-username", map[string]any{"addr": host, "uid": b.UID, "name": name}, nil); err != nil {
			return nil, fmt.Errorf("the PS5 didn't take the new name: %v", err)
		}
		return nil, nil
	})
	api("/api/prof/pick", func(r *http.Request) (any, error) {
		var b struct{ Mode string }
		body(r, &b)
		p, err := pickFile("Pick a profile picture", "Pictures", "*.png;*.jpg;*.jpeg;*.webp;*.bmp;*.gif")
		if err != nil || p == "" {
			return map[string]any{"path": ""}, err
		}
		prev, err := profPreview(p, b.Mode)
		if err != nil {
			return nil, err
		}
		return map[string]any{"path": p, "preview": prev}, nil
	})
	api("/api/prof/preview", func(r *http.Request) (any, error) {
		var b struct{ Path, Mode string }
		body(r, &b)
		prev, err := profPreview(b.Path, b.Mode)
		return map[string]any{"preview": prev}, err
	})
	api("/api/prof/picture", func(r *http.Request) (any, error) {
		var b struct {
			UID              uint32
			Name, Path, Mode string
		}
		body(r, &b)
		if !fileExists(b.Path) {
			return nil, errors.New("That picture file isn't there any more.")
		}
		base, host, err := a.profReady()
		if err != nil {
			return nil, err
		}
		req := map[string]any{"addr": host, "image_path": b.Path, "mode": profMode(b.Mode), "uid": b.UID}
		if b.Name != "" {
			req["username"] = b.Name
		}
		if err := p5uCall("POST", base+"/api/profile/avatar", req, nil); err != nil {
			return nil, fmt.Errorf("the PS5 didn't take the picture: %v", err)
		}
		return nil, nil
	})
}
