package main

import (
	"errors"
	"fmt"
	"net/url"
	"sync/atomic"
	"time"
)

// p5uSpaceOK is false when the user chose plain FTP only, so free space
// never wakes PS5Upload either.
var p5uSpaceOK atomic.Bool

// diskUsage gets the PS5's free space: from shsrv's "df" when that shell is
// running, otherwise from PS5Upload's helper (loading it if needed).
func diskUsage(host string, timeout time.Duration) ([]dfEntry, string, error) {
	df, err := shellDF(host, timeout)
	if err == nil {
		return df, "shsrv", nil
	}
	shellErr := err
	if !p5uSpaceOK.Load() || !p5uAvailable() {
		return nil, "", fmt.Errorf("shsrv on port %d: %v", shellPort, shellErr)
	}
	base, perr := p5uReady(host, nil)
	if perr == nil {
		var vl struct {
			Volumes []struct {
				Path        string `json:"path"`
				Total       int64  `json:"total_bytes"`
				Free        int64  `json:"free_bytes"`
				Placeholder bool   `json:"is_placeholder"`
			} `json:"volumes"`
		}
		if perr = p5uCall("GET", base+"/api/ps5/volumes?addr="+url.QueryEscape(host), nil, &vl); perr == nil {
			var out []dfEntry
			for _, v := range vl.Volumes {
				if v.Placeholder || v.Path == "" {
					continue
				}
				out = append(out, dfEntry{Mount: v.Path, Total: v.Total, Avail: v.Free})
			}
			if len(out) > 0 {
				return out, "PS5Upload", nil
			}
			perr = errors.New("it listed no drives")
		}
	}
	return nil, "", fmt.Errorf("shsrv on port %d: %v; PS5Upload: %v", shellPort, shellErr, perr)
}

// refreshSpace updates just the free space of the drives already known,
// without the slower drive and game scan, so it shows up quickly.
func (a *App) refreshSpace() {
	a.mu.Lock()
	host := a.cfg.PS5IP
	p5uSpaceOK.Store(!a.cfg.PlainFTP)
	online := a.ps5info.Online
	a.mu.Unlock()
	if host == "" || !online {
		return
	}
	df, src, err := diskUsage(host, 6*time.Second)
	a.mu.Lock()
	defer a.mu.Unlock()
	if host != a.cfg.PS5IP {
		return
	}
	if err != nil {
		a.ps5info.Shell, a.ps5info.SpaceErr = false, err.Error()
		return
	}
	a.ps5info.Shell, a.ps5info.SpaceFrom, a.ps5info.SpaceErr = true, src, ""
	for i := range a.ps5info.Drives {
		d := &a.ps5info.Drives[i]
		if e, ok := dfFor(df, d.Root); ok {
			d.Free, d.Total, d.Slack, d.Empty = e.Avail, e.Total, e.Slack, false
		}
	}
}

func (a *App) spaceLoop() {
	time.Sleep(5 * time.Second)
	for {
		a.refreshSpace()
		time.Sleep(20 * time.Second)
	}
}
