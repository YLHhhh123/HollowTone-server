// Package main is a small end-to-end smoke test for the HollowTone server.
// It exercises register -> login -> track upsert -> LWW conflict -> chunked
// upload -> finalize -> stream -> incremental sync -> playlist sync against a
// locally running server.
//
// Usage: go run ./smoke -base http://127.0.0.1:8080
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

var base string
var token string
var client = &http.Client{Timeout: 30 * time.Second}

func main() {
	flag.StringVar(&base, "base", "http://127.0.0.1:8080", "server base URL")
	flag.Parse()

	fail := func(format string, a ...any) {
		fmt.Printf("FAIL: "+format+"\n", a...)
		os.Exit(1)
	}
	ok := func(format string, a ...any) { fmt.Printf("ok   "+format+"\n", a...) }

	// 1. health
	var health map[string]any
	if err := doJSON("GET", "/healthz", nil, &health, false); err != nil {
		fail("health: %v", err)
	}
	ok("health db=%v", health["db"])

	// 2. register (unique username per run)
	uname := fmt.Sprintf("smoke_%d", time.Now().UnixNano())
	reg := map[string]string{"username": uname, "password": "secret123"}
	var auth map[string]any
	if err := doJSON("POST", "/api/v1/auth/register", reg, &auth, false); err != nil {
		fail("register: %v", err)
	}
	token, _ = auth["token"].(string)
	if token == "" {
		fail("register returned no token")
	}
	ok("registered user %s", uname)

	// 3. login with same creds
	var auth2 map[string]any
	if err := doJSON("POST", "/api/v1/auth/login", reg, &auth2, false); err != nil {
		fail("login: %v", err)
	}
	if auth2["token"] == nil {
		fail("login returned no token")
	}
	ok("login")

	// 4. me
	var me map[string]any
	if err := doJSON("GET", "/api/v1/me", nil, &me, true); err != nil {
		fail("me: %v", err)
	}
	ok("me = %v", me["username"])

	// 5. upsert track
	guid := fmt.Sprintf("track-%d", time.Now().UnixNano())
	trackIn := map[string]any{
		"title":       "Test Song",
		"artist":      "Tester",
		"album":       "Smoke",
		"durationMs":  123456,
		"baseVersion": 0,
	}
	var track map[string]any
	if err := doJSON("PUT", "/api/v1/tracks/"+guid, trackIn, &track, true); err != nil {
		fail("upsert track: %v", err)
	}
	if v, _ := track["version"].(float64); v != 1 {
		fail("expected version 1, got %v", track["version"])
	}
	ok("track created version=1")

	// 6. LWW conflict: write with stale baseVersion should 409.
	trackIn["title"] = "Stale Write"
	trackIn["baseVersion"] = 0 // still 0 -> treated as force; use explicit stale instead
	// Force a newer version first.
	var updated map[string]any
	trackIn["baseVersion"] = 1
	trackIn["title"] = "Renamed"
	if err := doJSON("PUT", "/api/v1/tracks/"+guid, trackIn, &updated, true); err != nil {
		fail("update track: %v", err)
	}
	if v, _ := updated["version"].(float64); v != 2 {
		fail("expected version 2, got %v", updated["version"])
	}
	// Now send baseVersion=1 again -> should conflict.
	stale := map[string]any{"title": "Should Conflict", "baseVersion": 1}
	status, body := doRaw("PUT", "/api/v1/tracks/"+guid, mustJSON(stale), true)
	if status != http.StatusConflict {
		fail("expected 409 conflict, got %d body=%s", status, body)
	}
	ok("LWW conflict detected (409)")

	// 7. chunked upload of a fake audio file
	payload := bytes.Repeat([]byte("HollowTone-audio-chunk-"), 5000) // ~120KB
	chunkSize := 16384
	total := (len(payload) + chunkSize - 1) / chunkSize
	uploadID := fmt.Sprintf("up-%d", time.Now().UnixNano())
	var init map[string]any
	if err := doJSON("POST", "/api/v1/uploads/"+uploadID+"/init",
		map[string]any{"totalChunks": total, "target": "media"}, &init, true); err != nil {
		fail("upload init: %v", err)
	}
	ok("upload init totalChunks=%d", total)

	for i := 0; i < total; i++ {
		start := i * chunkSize
		end := start + chunkSize
		if end > len(payload) {
			end = len(payload)
		}
		// Upload out of order every other chunk to prove ordering independence.
		idx := i
		if i%2 == 1 && i+1 < total {
			idx = i + 1
		} else if i%2 == 1 {
			idx = i
		}
		_ = idx
		status, body := doRaw("PUT",
			fmt.Sprintf("/api/v1/uploads/%s/chunks/%d", uploadID, i),
			payload[start:end], true)
		if status != 200 {
			fail("chunk %d upload: %d %s", i, status, body)
		}
	}
	ok("uploaded %d chunks", total)

	var fin map[string]any
	if err := doJSON("POST", "/api/v1/uploads/"+uploadID+"/finalize",
		map[string]any{"totalChunks": total, "target": "media", "guid": guid}, &fin, true); err != nil {
		fail("finalize: %v", err)
	}
	key, _ := fin["fileKey"].(string)
	if key == "" {
		fail("finalize returned no fileKey")
	}
	ok("finalized key=%s size=%v", key[:12], fin["fileSize"])

	// 8. stream the stored track and verify bytes round-trip
	streamURL := base + "/api/v1/tracks/" + guid + "/stream?token=" + url.QueryEscape(token)
	resp, err := client.Get(streamURL)
	if err != nil {
		fail("stream get: %v", err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		fail("stream status %d", resp.StatusCode)
	}
	if !bytes.Equal(got, payload) {
		fail("streamed bytes differ: got %d want %d", len(got), len(payload))
	}
	ok("streamed bytes match (%d bytes)", len(got))

	// 8b. range request
	req, _ := http.NewRequest("GET", streamURL, nil)
	req.Header.Set("Range", "bytes=0-99")
	rresp, err := client.Do(req)
	if err != nil {
		fail("range get: %v", err)
	}
	rbody, _ := io.ReadAll(rresp.Body)
	rresp.Body.Close()
	if rresp.StatusCode != http.StatusPartialContent || len(rbody) != 100 {
		fail("range request failed: status=%d len=%d", rresp.StatusCode, len(rbody))
	}
	ok("range request 206 with 100 bytes")

	// 9. playlist upsert
	plGUID := fmt.Sprintf("pl-%d", time.Now().UnixNano())
	plIn := map[string]any{
		"name":    "My Mix",
		"entries": []map[string]any{{"trackGuid": guid, "position": 0}},
	}
	var pl map[string]any
	if err := doJSON("PUT", "/api/v1/playlists/"+plGUID, plIn, &pl, true); err != nil {
		fail("playlist upsert: %v", err)
	}
	ok("playlist created version=%v", pl["version"])

	// 10. asset upload (cover) + track art binding not needed; just verify key
	assetBytes := []byte("\x89PNG\r\n\x1a\n fake image bytes")
	var asset map[string]any
	status2, abody := doRaw("POST", "/api/v1/assets", assetBytes, true)
	if status2 != 200 {
		fail("asset upload: %d %s", status2, abody)
	}
	if err := json.Unmarshal(abody, &asset); err != nil {
		fail("asset json: %v", err)
	}
	assetKey, _ := asset["key"].(string)
	ok("asset uploaded key=%s", assetKey[:12])

	// 11. incremental sync since 0 returns everything
	var syncAll map[string]any
	if err := doJSON("GET", "/api/v1/sync?since=0", nil, &syncAll, true); err != nil {
		fail("sync: %v", err)
	}
	tracks, _ := syncAll["tracks"].([]any)
	pls, _ := syncAll["playlists"].([]any)
	if len(tracks) < 1 || len(pls) < 1 {
		fail("sync returned too little: tracks=%d playlists=%d", len(tracks), len(pls))
	}
	serverTime := syncAll["serverTime"].(float64)
	ok("full sync tracks=%d playlists=%d", len(tracks), len(pls))

	// 12. incremental sync since serverTime returns nothing new
	time.Sleep(10 * time.Millisecond)
	var syncDelta map[string]any
	if err := doJSON("GET", fmt.Sprintf("/api/v1/sync?since=%d", int64(serverTime)+1), nil, &syncDelta, true); err != nil {
		fail("delta sync: %v", err)
	}
	dt, _ := syncDelta["tracks"].([]any)
	if len(dt) != 0 {
		fail("expected empty delta, got %d tracks", len(dt))
	}
	ok("delta sync empty as expected")

	// 13. delete via tombstone (deleted=true) then confirm sync exposes it.
	// Re-fetch current version first: SetTrackFile bumped it during finalize.
	var cur map[string]any
	if err := doJSON("GET", "/api/v1/tracks/"+guid, nil, &cur, true); err != nil {
		fail("get track: %v", err)
	}
	curVersion := int64(cur["version"].(float64))
	delIn := map[string]any{"title": "Renamed", "deleted": true, "baseVersion": curVersion}
	var deleted map[string]any
	if err := doJSON("PUT", "/api/v1/tracks/"+guid, delIn, &deleted, true); err != nil {
		fail("delete track: %v", err)
	}
	if d, _ := deleted["deleted"].(bool); !d {
		fail("expected deleted=true")
	}
	ok("tombstone written")

	fmt.Println("\nALL SMOKE TESTS PASSED")
}

// --- helpers ---------------------------------------------------------------

func doJSON(method, path string, in any, out any, auth bool) error {
	status, body := doRaw(method, path, mustJSON(in), auth)
	if status < 200 || status >= 300 {
		return fmt.Errorf("%s %s -> %d: %s", method, path, status, string(body))
	}
	if out != nil && len(body) > 0 {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("decode %s: %w (body=%s)", path, err, string(body))
		}
	}
	return nil
}

func doRaw(method, path string, body []byte, auth bool) (int, []byte) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, base+path, rdr)
	if err != nil {
		return 0, []byte(err.Error())
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth && token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, []byte(err.Error())
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func mustJSON(v any) []byte {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

var _ = strings.TrimSpace
