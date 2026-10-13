package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func storeTestApp(t *testing.T) (*App, *httptest.Server) {
	t.Helper()
	a := &App{saved: map[string]string{}, lib: &Library{Games: map[string]*LibEntry{}}, sized: map[string]bool{}, sent: map[string]SentInfo{}}
	a.cfg.RemotePIN = "4321"
	rs := &remoteServer{token: randHex(24)}
	srv := httptest.NewServer(a.remoteHandler(rs))
	t.Cleanup(srv.Close)
	return a, srv
}

func storeCall(t *testing.T, method, url, token string, body any) (int, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, url, rd)
	if token != "" {
		req.Header.Set("X-Loadout-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func TestStorePairing(t *testing.T) {
	a, srv := storeTestApp(t)

	// nothing works before pairing
	if code, m := storeCall(t, "GET", srv.URL+"/s/games", "", nil); code != 401 || m["pair"] != true {
		t.Fatalf("unpaired games: %d %v", code, m)
	}
	if _, m := storeCall(t, "GET", srv.URL+"/s/hello", "", nil); m["app"] != "Loadout" || m["paired"] != false {
		t.Fatalf("hello: %v", m)
	}
	if code, _ := storeCall(t, "POST", srv.URL+"/s/pair", "", map[string]string{"PIN": "0000", "Name": "PS5"}); code != 401 {
		t.Fatalf("wrong PIN paired: %d", code)
	}

	code, m := storeCall(t, "POST", srv.URL+"/s/pair", "", map[string]string{"PIN": "4321", "Name": "Living room PS5"})
	tok, _ := m["token"].(string)
	if code != 200 || len(tok) != 64 {
		t.Fatalf("pair: %d %v", code, m)
	}
	if len(a.cfg.StoreDevices) != 1 || a.cfg.StoreDevices[0].Name != "Living room PS5" || strings.Contains(a.cfg.StoreDevices[0].Hash, tok) {
		t.Fatalf("device not saved right: %+v", a.cfg.StoreDevices)
	}

	if code, m := storeCall(t, "GET", srv.URL+"/s/games", tok, nil); code != 200 || m["games"] == nil {
		t.Fatalf("paired games: %d %v", code, m)
	}
	if _, m := storeCall(t, "GET", srv.URL+"/s/hello", tok, nil); m["paired"] != true {
		t.Fatalf("hello with token: %v", m)
	}
	if code, _ := storeCall(t, "GET", srv.URL+"/s/status", tok, nil); code != 200 {
		t.Fatalf("status: %d", code)
	}
	if code, _ := storeCall(t, "GET", srv.URL+"/s/games", tok[:63]+"x", nil); code != 401 {
		t.Fatalf("a wrong token got in: %d", code)
	}
	// asking for a game that isn't there says so instead of failing silently
	if _, m := storeCall(t, "POST", srv.URL+"/s/get", tok, map[string]string{"Key": "nope"}); m["error"] == nil {
		t.Fatalf("get of a missing game: %v", m)
	}

	// forgetting the device locks it out again
	a.forgetStoreDeviceLocked(a.cfg.StoreDevices[0].ID)
	if code, _ := storeCall(t, "GET", srv.URL+"/s/games", tok, nil); code != 401 {
		t.Fatalf("forgotten device still works: %d", code)
	}
}

func TestStorePINLockout(t *testing.T) {
	_, srv := storeTestApp(t)
	var code int
	for i := 0; i < 5; i++ {
		code, _ = storeCall(t, "POST", srv.URL+"/s/pair", "", map[string]string{"PIN": "1111"})
	}
	if code != 401 {
		t.Fatalf("5th wrong PIN: %d", code)
	}
	// locked now, even with the right PIN
	if code, _ := storeCall(t, "POST", srv.URL+"/s/pair", "", map[string]string{"PIN": "4321"}); code != 429 {
		t.Fatalf("not locked out: %d", code)
	}
}

func TestStoreDiscovery(t *testing.T) {
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skip("no UDP here:", err)
	}
	defer pc.Close()
	go serveDiscovery(pc, 8787)
	c, err := net.Dial("udp4", pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("hello?")) // anything else is ignored
	c.Write([]byte(discoverAsk))
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 512)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if json.Unmarshal(buf[:n], &m); m["app"] != "Loadout" || m["port"] != float64(8787) {
		t.Fatalf("reply: %s", buf[:n])
	}
}
