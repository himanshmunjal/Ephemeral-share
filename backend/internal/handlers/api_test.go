package handlers_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"ephemeral-share/internal/handlers"
)

func TestCreateSession(t *testing.T) {
	e := newEnv(t)
	r := e.do("POST", "/api/v1/sessions", map[string]any{"type": "shared_workspace", "max_participants": 50})
	if r.Status != 201 {
		t.Fatalf("status %d", r.Status)
	}
	code := r.Body["code"].(string)
	if len(code) != 8 {
		t.Errorf("code length = %d, want 8", len(code))
	}
	if r.Body["owner_token"] == "" {
		t.Error("owner token missing from create response")
	}

	got := e.do("GET", "/api/v1/sessions/"+code, nil)
	if got.Status != 200 {
		t.Fatalf("get: %d", got.Status)
	}
	if got.Body["max_participants"].(float64) != 2 {
		t.Errorf("max_participants = %v, want clamped to server cap 2", got.Body["max_participants"])
	}
	if _, leaked := got.Body["owner_token"]; leaked {
		t.Error("owner token leaked from GET /sessions/:code")
	}
	limits := got.Body["limits"].(map[string]any)
	if limits["max_files"].(float64) != 3 {
		t.Errorf("limits not exposed: %v", limits)
	}
	if e.do("POST", "/api/v1/sessions", map[string]any{"type": "bogus"}).Status != 400 {
		t.Error("invalid type should be 400")
	}
}

func TestUploadValidationAndQuotas(t *testing.T) {
	e := newEnv(t)
	code, _ := e.newSession("quick_transfer")

	r := e.do("POST", fmt.Sprintf("/api/v1/sessions/%s/content/upload-url", code),
		map[string]any{"filename": "x", "content_type": "text/html", "size_bytes": 10}, "X-Participant-ID", alice)
	if r.Status != http.StatusUnsupportedMediaType {
		t.Errorf("non-opaque content type: %d, want 415", r.Status)
	}
	if r := e.requestUpload(code, alice, 2<<20); r.Status != 413 || r.Body["code"] != "file_too_large" {
		t.Errorf("per-file limit: %d %v", r.Status, r.Body)
	}

	// Session total is 2 MiB: two 900 KiB files fit, a third doesn't.
	first := e.requestUpload(code, alice, 900<<10)
	second := e.requestUpload(code, alice, 900<<10)
	if first.Status != 200 || second.Status != 200 {
		t.Fatalf("expected two uploads to fit: %d %d", first.Status, second.Status)
	}
	if r := e.requestUpload(code, alice, 900<<10); r.Status != 413 || r.Body["code"] != "session_full" {
		t.Errorf("session byte quota: %d %v", r.Status, r.Body)
	}

	// Cancelling a pending upload frees its quota.
	id := second.Body["content_id"].(string)
	if r := e.do("DELETE", fmt.Sprintf("/api/v1/sessions/%s/content/%s", code, id), nil, "X-Participant-ID", alice); r.Status != 200 {
		t.Fatalf("cancel pending: %d", r.Status)
	}
	if r := e.requestUpload(code, alice, 900<<10); r.Status != 200 {
		t.Errorf("quota not released after cancel: %d", r.Status)
	}
}

func TestFileCountLimit(t *testing.T) {
	e := newEnv(t)
	code, _ := e.newSession("quick_transfer")
	for i := 0; i < 3; i++ {
		if r := e.requestUpload(code, alice, 10); r.Status != 200 {
			t.Fatalf("upload %d: %d", i, r.Status)
		}
	}
	if r := e.requestUpload(code, alice, 10); r.Status != 409 || r.Body["code"] != "too_many_files" {
		t.Errorf("file count limit: %d %v", r.Status, r.Body)
	}
}

func TestUploadCompletesAndVerifiesSize(t *testing.T) {
	e := newEnv(t)
	code, _ := e.newSession("quick_transfer")

	id, done := e.upload(code, alice, 1000)
	if done.Status != 201 {
		t.Fatalf("complete: %d %v", done.Status, done.Body)
	}
	list := e.do("GET", fmt.Sprintf("/api/v1/sessions/%s/content", code), nil, "X-Participant-ID", alice)
	items := list.Body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["mine"] != true {
		t.Fatalf("feed wrong: %v", list.Body)
	}
	if _, leaked := items[0].(map[string]any)["uploader_id"]; leaked {
		t.Error("uploader id leaked to clients")
	}
	other := e.do("GET", fmt.Sprintf("/api/v1/sessions/%s/content", code), nil, "X-Participant-ID", bob)
	if other.Body["items"].([]any)[0].(map[string]any)["mine"] == true {
		t.Error("another participant must not see mine=true")
	}

	// Downloads are forced to attachment.
	dl := e.do("GET", fmt.Sprintf("/api/v1/sessions/%s/content/%s/download?redirect=false", code, id), nil)
	if u, _ := dl.Body["download_url"].(string); !strings.Contains(u, "response-content-disposition=attachment") {
		t.Errorf("download URL doesn't force attachment: %v", dl.Body)
	}

	// A body that doesn't match the declared size is rejected and its quota freed.
	r := e.requestUpload(code, alice, 500)
	e.putBytes(r.Body["upload_url"].(string), 100)
	bad := e.do("POST", fmt.Sprintf("/api/v1/sessions/%s/content/%s/complete", code, r.Body["content_id"]), nil, "X-Participant-ID", alice)
	if bad.Status != 413 {
		t.Errorf("size mismatch: %d, want 413", bad.Status)
	}
}

func TestOnlyUploaderCanDelete(t *testing.T) {
	e := newEnv(t)
	code, owner := e.newSession("shared_workspace")
	id, done := e.upload(code, alice, 100)
	if done.Status != 201 {
		t.Fatalf("setup upload failed: %d", done.Status)
	}
	path := fmt.Sprintf("/api/v1/sessions/%s/content/%s", code, id)

	if r := e.do("DELETE", path, nil, "X-Participant-ID", bob); r.Status != 403 {
		t.Errorf("other participant: %d, want 403", r.Status)
	}
	if r := e.do("DELETE", path, nil, "X-Participant-ID", bob, "X-Owner-Token", owner); r.Status != 403 {
		t.Errorf("owner who isn't the uploader: %d, want 403", r.Status)
	}
	if r := e.do("DELETE", path, nil); r.Status != 403 {
		t.Errorf("anonymous: %d, want 403", r.Status)
	}
	if r := e.do("DELETE", path, nil, "X-Participant-ID", alice); r.Status != 200 {
		t.Errorf("uploader: %d, want 200", r.Status)
	}
	if e.s3.has("sessions/" + code + "/") {
		t.Error("object not removed from storage")
	}
	if r := e.do("GET", path[:strings.LastIndex(path, "/")], nil); len(r.Body["items"].([]any)) != 0 {
		t.Error("item still in feed")
	}
}

func TestTextNotesCarryEncryptedIdentity(t *testing.T) {
	e := newEnv(t)
	code, _ := e.newSession("shared_workspace")
	path := fmt.Sprintf("/api/v1/sessions/%s/content/text", code)
	who := "AbCdEfGhIjKlMnOpQrStUvWx-_0123456789"

	r := e.do("POST", path, map[string]any{"content_type": "note", "text": "Y2lwaGVy"}, "X-Participant-ID", alice, "X-Participant-Who", who)
	if r.Status != 201 || r.Body["who"] != who {
		t.Fatalf("valid identity should be kept: %d %v", r.Status, r.Body)
	}
	r = e.do("POST", path, map[string]any{"content_type": "note", "text": "Y2lwaGVy"}, "X-Participant-ID", alice, "X-Participant-Who", "<script>alert(1)</script>")
	if r.Status != 201 || r.Body["who"] != nil {
		t.Errorf("malformed identity must be dropped, got %v", r.Body["who"])
	}
}

func TestWrongCodeLockout(t *testing.T) {
	e := newEnv(t)
	code, _ := e.newSession("quick_transfer")

	for i := 0; i < 5; i++ {
		if r := e.do("GET", "/api/v1/sessions/NOPE0000", nil); r.Status != 404 {
			t.Fatalf("miss %d: %d", i, r.Status)
		}
	}
	// Locked out: even a valid code is refused until the window passes.
	r := e.do("GET", "/api/v1/sessions/"+code, nil)
	if r.Status != 429 || r.Body["code"] != "locked_out" || r.Header.Get("Retry-After") == "" {
		t.Errorf("lockout: %d %v retry-after=%q", r.Status, r.Body, r.Header.Get("Retry-After"))
	}
	e.mr.FastForward(e.cfg.LockoutDuration + time.Second)
	if r := e.do("GET", "/api/v1/sessions/"+code, nil); r.Status != 200 {
		t.Errorf("lockout should expire, got %d", r.Status)
	}
}

func TestUploadRateLimit(t *testing.T) {
	e := newEnv(t, "UPLOAD_RATE_LIMIT_PER_MIN=3")
	code, _ := e.newSession("quick_transfer")
	var statuses []int
	for i := 0; i < 5; i++ {
		statuses = append(statuses, e.requestUpload(code, alice, 10).Status)
	}
	if statuses[3] != 429 || statuses[4] != 429 {
		t.Errorf("expected the 4th and 5th uploads to be rate limited, got %v", statuses)
	}
}

func TestHealthHeadersAndCORS(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/health", "/healthz"} {
		if r := e.do("GET", p, nil); r.Status != 200 {
			t.Errorf("%s: %d", p, r.Status)
		}
	}
	r := e.do("GET", "/api/v1/sessions/NOPE0000", nil)
	for _, h := range []string{"X-Content-Type-Options", "X-Frame-Options", "Content-Security-Policy", "Referrer-Policy"} {
		if r.Header.Get(h) == "" {
			t.Errorf("missing security header %s", h)
		}
	}

	preflight := func(origin string) *http.Response {
		req, _ := http.NewRequest("OPTIONS", e.api.URL+"/api/v1/sessions", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "x-owner-token,x-participant-id,x-participant-who,content-type")
		res, _ := http.DefaultClient.Do(req)
		res.Body.Close()
		return res
	}
	if res := preflight(testOrigin); res.Header.Get("Access-Control-Allow-Origin") != testOrigin {
		t.Errorf("configured origin rejected: %d", res.StatusCode)
	}
	if res := preflight("https://evil.example"); res.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Error("foreign origin must not be allowed")
	}

	// With the dependency down, health must say so (so the platform restarts us).
	e.mr.Close()
	if r := e.do("GET", "/health", nil); r.Status != 503 {
		t.Errorf("health with Redis down: %d, want 503", r.Status)
	}
}

func TestAdminTakedown(t *testing.T) {
	token := strings.Repeat("a", 32)
	e := newEnv(t, "ADMIN_TOKEN="+token)
	code, _ := e.newSession("quick_transfer")
	e.upload(code, alice, 100)

	if r := e.do("DELETE", "/admin/sessions/"+code, nil); r.Status != 403 {
		t.Errorf("no token: %d", r.Status)
	}
	if r := e.do("DELETE", "/admin/sessions/"+code, nil, "X-Admin-Token", "wrong"); r.Status != 403 {
		t.Errorf("wrong token: %d", r.Status)
	}
	if r := e.do("DELETE", "/admin/sessions/"+code, nil, "X-Admin-Token", token); r.Status != 200 {
		t.Errorf("valid takedown: %d", r.Status)
	}
	if e.s3.has("sessions/" + code + "/") {
		t.Error("takedown left files behind")
	}
	if r := e.do("GET", "/api/v1/sessions/"+code, nil); r.Status != 404 {
		t.Errorf("session still live after takedown: %d", r.Status)
	}

	// Without ADMIN_TOKEN the route doesn't exist at all.
	// Without ADMIN_TOKEN the route must not exist at all: gin's plain-text 404,
	// not our JSON "session not found" (which would mean the route is live).
	if r := newEnv(t).do("DELETE", "/admin/sessions/ABCDEFGH", nil, "X-Admin-Token", token); r.Status != 404 || r.Body != nil {
		t.Errorf("admin route should be disabled by default, got %d %v", r.Status, r.Body)
	}
}

func TestOrphanSweepRemovesOnlyDeadSessionsFiles(t *testing.T) {
	e := newEnv(t)
	live, _ := e.newSession("quick_transfer")
	e.s3.plant("sessions/"+live+"/id/enc", time.Hour)
	e.s3.plant("sessions/DEAD0000/id/enc", time.Hour) // session gone → orphan
	e.s3.plant("sessions/FRESH000/id/enc", 0)         // too young to judge (grace)

	worker := handlers.NewCleanupWorker(nil, e.sc, nil, e.store)
	if n := worker.SweepOrphansOnce(t.Context(), 30*time.Minute); n != 1 {
		t.Errorf("deleted %d objects, want exactly 1", n)
	}
	if e.s3.has("sessions/DEAD0000/") {
		t.Error("orphan not removed")
	}
	if !e.s3.has("sessions/"+live+"/") || !e.s3.has("sessions/FRESH000/") {
		t.Error("sweep deleted something it shouldn't have")
	}
}

// ---- WebSocket presence ---------------------------------------------------

func dial(t *testing.T, e *env, code string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	u := "ws" + strings.TrimPrefix(e.api.URL, "http") + "/ws/sessions/" + code
	c, res, err := websocket.DefaultDialer.Dial(u, http.Header{"Origin": {testOrigin}})
	if c != nil {
		t.Cleanup(func() { c.Close() })
	}
	return c, res, err
}

// waitPresence reads presence events until the count reaches `want`. Updates
// travel through Redis pub/sub, so a client may briefly see an earlier count
// first; what matters is that it converges on the right one.
func waitPresence(t *testing.T, c *websocket.Conn, want int) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	var seen []int
	for {
		var m map[string]any
		if err := c.ReadJSON(&m); err != nil {
			t.Fatalf("presence never reached %d (saw %v): %v", want, seen, err)
		}
		if m["event"] != "presence" {
			continue
		}
		n := int(m["count"].(float64))
		seen = append(seen, n)
		if n == want {
			return
		}
	}
}

func TestPresenceCapAndLiveCount(t *testing.T) {
	e := newEnv(t) // MAX_PARTICIPANTS=2
	code, _ := e.newSession("shared_workspace")

	a, _, err := dial(t, e, code)
	if err != nil {
		t.Fatal(err)
	}
	waitPresence(t, a, 1)
	b, _, err := dial(t, e, code)
	if err != nil {
		t.Fatal(err)
	}
	waitPresence(t, b, 2)
	waitPresence(t, a, 2) // the first joiner is told about the second

	// Third connection is turned away, both at the socket and the pre-check.
	if _, res, err := dial(t, e, code); err == nil || res == nil || res.StatusCode != http.StatusConflict {
		t.Errorf("third connection should be rejected with 409 (err=%v res=%v)", err, res)
	}
	if r := e.do("POST", "/api/v1/sessions/"+code+"/join", nil); r.Status != 409 || r.Body["code"] != "full" {
		t.Errorf("join pre-check: %d %v", r.Status, r.Body)
	}

	// Leaving frees the slot and updates everyone else.
	b.Close()
	waitPresence(t, a, 1)
	if _, _, err := dial(t, e, code); err != nil {
		t.Errorf("slot should be reusable after a leave: %v", err)
	}
}

func TestWebSocketRejectsForeignOrigin(t *testing.T) {
	e := newEnv(t)
	code, _ := e.newSession("shared_workspace")
	u := "ws" + strings.TrimPrefix(e.api.URL, "http") + "/ws/sessions/" + code
	_, res, err := websocket.DefaultDialer.Dial(u, http.Header{"Origin": {"https://evil.example"}})
	if err == nil || res == nil || res.StatusCode != http.StatusForbidden {
		t.Errorf("foreign origin should get 403, err=%v res=%v", err, res)
	}
}

func TestEndingSessionNotifiesClients(t *testing.T) {
	e := newEnv(t)
	code, owner := e.newSession("shared_workspace")
	c, _, err := dial(t, e, code)
	if err != nil {
		t.Fatal(err)
	}
	waitPresence(t, c, 1)
	if r := e.do("DELETE", "/api/v1/sessions/"+code, nil, "X-Owner-Token", "wrong"); r.Status != 403 {
		t.Fatalf("wrong owner token: %d", r.Status)
	}
	if r := e.do("DELETE", "/api/v1/sessions/"+code, nil, "X-Owner-Token", owner); r.Status != 200 {
		t.Fatalf("owner end: %d", r.Status)
	}
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		var m map[string]any
		if err := c.ReadJSON(&m); err != nil {
			t.Fatalf("never received session_ended: %v", err)
		}
		if m["event"] == "session_ended" {
			return
		}
	}
}

// ---- client IP behind a proxy ---------------------------------------------

func TestClientIPBehindProxy(t *testing.T) {
	e := newEnv(t, "TRUSTED_PROXIES=private") // the test client connects from loopback
	code, _ := e.newSession("quick_transfer")

	miss := func(xff string) {
		e.do("GET", "/api/v1/sessions/NOPE0000", nil, "X-Forwarded-For", xff)
	}
	// One real client (9.9.9.9) burns its budget...
	for i := 0; i < 5; i++ {
		miss("9.9.9.9")
	}
	if r := e.do("GET", "/api/v1/sessions/"+code, nil, "X-Forwarded-For", "9.9.9.9"); r.Status != 429 {
		t.Errorf("the offending client should be locked out, got %d", r.Status)
	}
	// ...without locking out a different client behind the same proxy.
	if r := e.do("GET", "/api/v1/sessions/"+code, nil, "X-Forwarded-For", "8.8.8.8"); r.Status != 200 {
		t.Errorf("an unrelated client must not be affected, got %d", r.Status)
	}
	// A spoofed leading entry must not let the offender dodge the lockout:
	// the platform appends the real address on the right.
	if r := e.do("GET", "/api/v1/sessions/"+code, nil, "X-Forwarded-For", "7.7.7.7, 9.9.9.9"); r.Status != 429 {
		t.Errorf("spoofed X-Forwarded-For prefix bypassed the lockout: %d", r.Status)
	}
}

func TestClientIPIgnoresForwardedHeadersByDefault(t *testing.T) {
	e := newEnv(t) // no trusted proxies: headers are attacker-controlled
	code, _ := e.newSession("quick_transfer")
	for i := 0; i < 5; i++ {
		e.do("GET", "/api/v1/sessions/NOPE0000", nil, "X-Forwarded-For", fmt.Sprintf("1.1.1.%d", i))
	}
	if r := e.do("GET", "/api/v1/sessions/"+code, nil, "X-Forwarded-For", "2.2.2.2"); r.Status != 429 {
		t.Errorf("rotating X-Forwarded-For must not evade the lockout when no proxy is trusted: %d", r.Status)
	}
}

// ---- SIGN_CONTENT_LENGTH --------------------------------------------------

func signedHeaders(t *testing.T, e *env) string {
	t.Helper()
	code, _ := e.newSession("quick_transfer")
	r := e.requestUpload(code, alice, 1000)
	if r.Status != 200 {
		t.Fatalf("upload-url: %d %v", r.Status, r.Body)
	}
	u, err := url.Parse(r.Body["upload_url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ToLower(u.Query().Get("X-Amz-SignedHeaders"))
}

func TestContentLengthIsSignedByDefault(t *testing.T) {
	if h := signedHeaders(t, newEnv(t)); !strings.Contains(h, "content-length") {
		t.Errorf("declared size should be bound into the URL by default; signed headers = %q", h)
	}
}

func TestContentLengthSigningCanBeDisabledForPickyStorage(t *testing.T) {
	e := newEnv(t, "SIGN_CONTENT_LENGTH=false")
	if h := signedHeaders(t, e); strings.Contains(h, "content-length") {
		t.Errorf("content-length must not be signed when disabled; signed headers = %q", h)
	}
	// With signing off, the size check at completion is the safety net:
	// a body bigger than declared must still be rejected.
	code, _ := e.newSession("quick_transfer")
	r := e.requestUpload(code, alice, 100)
	e.putBytes(r.Body["upload_url"].(string), 5000)
	bad := e.do("POST", fmt.Sprintf("/api/v1/sessions/%s/content/%s/complete", code, r.Body["content_id"]), nil, "X-Participant-ID", alice)
	if bad.Status != 413 {
		t.Errorf("oversized body slipped through completion: %d", bad.Status)
	}
	if e.s3.has("sessions/" + code + "/") {
		t.Error("the mismatched object should have been deleted")
	}
}

func TestClientIPBehindCloudflare(t *testing.T) {
	e := newEnv(t, "TRUSTED_PROXIES=private,cloudflare")
	code, _ := e.newSession("quick_transfer")
	// Render-style chain: "<visitor>, <cloudflare edge>" arriving via a private hop.
	chain := func(visitor string) string { return visitor + ", 104.22.72.53" }

	for i := 0; i < 5; i++ {
		e.do("GET", "/api/v1/sessions/NOPE0000", nil, "X-Forwarded-For", chain("9.9.9.9"))
	}
	if r := e.do("GET", "/api/v1/sessions/"+code, nil, "X-Forwarded-For", chain("9.9.9.9")); r.Status != 429 {
		t.Errorf("the offending visitor should be locked out, got %d", r.Status)
	}
	// Another visitor on the SAME Cloudflare edge must not be affected.
	if r := e.do("GET", "/api/v1/sessions/"+code, nil, "X-Forwarded-For", chain("8.8.8.8")); r.Status != 200 {
		t.Errorf("a different visitor behind the same edge was blocked: %d", r.Status)
	}
}

func TestIPDiagnosticsAreAdminOnlyAndShowResolvedClient(t *testing.T) {
	token := strings.Repeat("d", 32)
	e := newEnv(t, "ADMIN_TOKEN="+token, "TRUSTED_PROXIES=private,cloudflare")
	if r := e.do("GET", "/admin/diag/ip", nil); r.Status != 403 {
		t.Errorf("no token: %d, want 403", r.Status)
	}
	r := e.do("GET", "/admin/diag/ip", nil, "X-Admin-Token", token, "X-Forwarded-For", "7.7.7.7, 104.22.72.53")
	if r.Status != 200 || r.Body["client_ip"] != "7.7.7.7" {
		t.Errorf("diagnostics should report the real visitor, got %d %v", r.Status, r.Body)
	}
	if r := newEnv(t).do("GET", "/admin/diag/ip", nil, "X-Admin-Token", token); r.Status != 404 {
		t.Errorf("route must not exist without ADMIN_TOKEN, got %d", r.Status)
	}
}

// ---- performance budget -----------------------------------------------------

// Against a remote Redis every round trip costs real time (~0.4s measured from
// Render to Upstash), so the number of round trips per request IS the latency.
// These budgets pin the hot paths; raising one should be a deliberate decision.
func TestRedisRoundTripBudgets(t *testing.T) {
	e := newEnv(t)
	var n atomic.Int64
	e.store.CountRoundTrips(&n)

	code, owner := e.newSession("shared_workspace")
	base := "/api/v1/sessions/" + code
	measure := func(f func()) int64 {
		before := n.Load()
		f()
		return n.Load() - before
	}
	budget := func(name string, max int64, f func()) {
		t.Helper()
		f() // warm-up: the first call of each script costs an extra EVALSHA→EVAL
		if got := measure(f); got > max {
			t.Errorf("%s used %d Redis round trips, budget is %d", name, got, max)
		} else {
			t.Logf("%-22s %d round trip(s) (budget %d)", name, got, max)
		}
	}

	budget("GET session", 1, func() { e.do("GET", base, nil) })
	budget("POST join", 1, func() { e.do("POST", base+"/join", nil) })

	// A feed with several items must still cost a constant number of trips.
	for i := 0; i < 6; i++ {
		e.do("POST", base+"/content/text", map[string]any{"content_type": "note", "text": "Y2lwaGVy"}, "X-Participant-ID", alice)
	}
	budget("GET content (6 items)", 2, func() { e.do("GET", base+"/content", nil, "X-Participant-ID", alice) })
	budget("POST text note", 3, func() {
		e.do("POST", base+"/content/text", map[string]any{"content_type": "note", "text": "Y2lwaGVy"}, "X-Participant-ID", alice)
	})
	// upload-url: measure only the request itself, then cancel to free quota.
	for run := 0; run < 2; run++ { // first run is the warm-up
		before := n.Load()
		r := e.requestUpload(code, alice, 10)
		got := n.Load() - before
		if id, ok := r.Body["content_id"].(string); ok {
			e.do("DELETE", base+"/content/"+id, nil, "X-Participant-ID", alice)
		}
		if run == 1 {
			if got > 3 {
				t.Errorf("POST upload-url used %d Redis round trips, budget is 3", got)
			} else {
				t.Logf("%-22s %d round trip(s) (budget 3)", "POST upload-url", got)
			}
		}
	}
	_ = owner
}

func TestFeedLoadDoesNotGrowWithItemCount(t *testing.T) {
	e := newEnv(t, "MAX_TEXT_ITEMS_PER_SESSION=100")
	var n atomic.Int64
	e.store.CountRoundTrips(&n)
	code, _ := e.newSession("quick_transfer")
	path := "/api/v1/sessions/" + code + "/content"
	cost := func() int64 {
		e.do("GET", path, nil) // warm
		before := n.Load()
		e.do("GET", path, nil)
		return n.Load() - before
	}
	empty := cost()
	for i := 0; i < 30; i++ {
		e.do("POST", path+"/text", map[string]any{"content_type": "note", "text": "Y2lwaGVy"})
	}
	if full := cost(); full != empty && full > 2 {
		t.Errorf("loading 30 items cost %d round trips vs %d when empty — the feed must not do one lookup per item", full, empty)
	}
}
