// Command preflight checks that your real Redis and object-storage settings
// work with this app BEFORE you deploy: connectivity, TLS, Lua, pub/sub,
// keyspace events, pre-signed uploads/downloads, CORS and the signed-size
// setting. It never prints credentials.
//
//	cd backend
//	go run ./cmd/preflight -env .env.cloud
//	go run ./cmd/preflight -env .env.cloud -only redis
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"

	"ephemeral-share/internal/config"
	"ephemeral-share/internal/redisclient"
	"ephemeral-share/internal/storage"
)

var failures, warnings int

func pass(f string, a ...any) { fmt.Printf("  ✔ %s\n", fmt.Sprintf(f, a...)) }
func fail(f string, a ...any) { failures++; fmt.Printf("  ✘ %s\n", fmt.Sprintf(f, a...)) }
func warn(f string, a ...any) { warnings++; fmt.Printf("  ! %s\n", fmt.Sprintf(f, a...)) }
func info(f string, a ...any) { fmt.Printf("    %s\n", fmt.Sprintf(f, a...)) }

func main() {
	envFile := flag.String("env", ".env", "env file to load (values already in the environment win)")
	only := flag.String("only", "all", "what to check: all | redis | storage")
	setCORS := flag.Bool("set-cors", false, "replace the bucket's CORS rules so ALLOWED_ORIGIN can upload/download from the browser")
	flag.Parse()

	if err := godotenv.Load(*envFile); err != nil {
		fmt.Printf("note: could not read %s (%v) — using the process environment only\n", *envFile, err)
	}
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if *only == "all" || *only == "redis" {
		checkRedis(ctx, cfg)
	}
	if *only == "all" || *only == "storage" {
		checkStorage(ctx, cfg, *setCORS)
	}

	fmt.Println()
	switch {
	case failures > 0:
		fmt.Printf("RESULT: %d check(s) failed, %d warning(s). Fix the ✘ items before deploying.\n", failures, warnings)
		os.Exit(1)
	case warnings > 0:
		fmt.Printf("RESULT: all required checks passed, with %d warning(s) worth reading.\n", warnings)
	default:
		fmt.Println("RESULT: all checks passed.")
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- Redis ----------------------------------------------------------------

func checkRedis(ctx context.Context, cfg *config.Config) {
	fmt.Printf("\nRedis  (%s, TLS %v)\n", cfg.RedisAddr, cfg.RedisTLS)
	rdb := redis.NewClient(redisclient.Options(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisTLS))
	defer rdb.Close()

	if err := rdb.Ping(ctx).Err(); err != nil {
		fail("cannot connect: %v", err)
		if !cfg.RedisTLS && strings.Contains(cfg.RedisAddr, "upstash") {
			info("Upstash only accepts TLS — set REDIS_TLS=true")
		}
		return
	}
	pass("connected and authenticated")

	key := "preflight:" + randHex(4)
	if err := rdb.Set(ctx, key, "ok", 30*time.Second).Err(); err != nil {
		fail("SET with expiry failed: %v", err)
	} else if v, _ := rdb.Get(ctx, key).Result(); v == "ok" {
		pass("SET/GET with expiry")
	}
	_ = rdb.Del(ctx, key)

	if n, err := rdb.Eval(ctx, "return redis.call('INCR', KEYS[1])", []string{key}).Int(); err != nil || n != 1 {
		fail("Lua scripting failed (quotas and presence need it): %v", err)
	} else {
		pass("Lua scripting (used for quotas, presence)")
	}
	_ = rdb.Del(ctx, key)

	ch := "preflight:ch:" + randHex(4)
	sub := rdb.Subscribe(ctx, ch)
	if _, err := sub.Receive(ctx); err != nil {
		fail("pub/sub subscribe failed: %v", err)
	} else {
		rdb.Publish(ctx, ch, "ping")
		select {
		case m := <-sub.Channel():
			if m.Payload == "ping" {
				pass("pub/sub round trip (used for live updates)")
			}
		case <-time.After(4 * time.Second):
			fail("pub/sub message never arrived")
		}
	}
	sub.Close()

	// Expired-key events: the cleanup worker depends on them.
	if err := rdb.ConfigSet(ctx, "notify-keyspace-events", "Ex").Err(); err != nil {
		warn("CONFIG SET notify-keyspace-events refused: %v", err)
	}
	ps := rdb.PSubscribe(ctx, "__keyevent@0__:expired")
	if _, err := ps.Receive(ctx); err != nil {
		warn("could not subscribe to expiry events: %v", err)
	} else {
		ek := "preflight:exp:" + randHex(4)
		rdb.Set(ctx, ek, "x", 1*time.Second)
		select {
		case m := <-ps.Channel():
			if strings.HasPrefix(m.Payload, "preflight:exp:") {
				pass("expired-key events are delivered (instant cleanup works)")
			}
		case <-time.After(15 * time.Second):
			warn("no expiry event within 15s — this host doesn't deliver them reliably.")
			info("The app still works: clients end the session from their countdown and the orphan sweep deletes files")
			info("(every ORPHAN_SWEEP_SECONDS). Consider ORPHAN_SWEEP_SECONDS=300 so files are removed sooner.")
		}
	}
	ps.Close()
}

// ---- Storage ----------------------------------------------------------------

func checkStorage(ctx context.Context, cfg *config.Config, setCORS bool) {
	fmt.Printf("\nStorage  (endpoint %s, bucket %q, region %s)\n", cfg.S3Endpoint, cfg.S3Bucket, cfg.S3Region)
	if cfg.S3AccessKey == "" || cfg.S3AccessKey == "minioadmin" {
		fail("S3_ACCESS_KEY is empty or still the MinIO dev default — set your bucket's application key ID")
		return
	}
	sc, err := storage.New(ctx, cfg.S3Endpoint, cfg.S3PublicEndpoint, cfg.S3Region, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3Bucket)
	if err != nil {
		fail("client setup failed: %v", err)
		return
	}
	if err := sc.Ping(ctx); err != nil {
		warn("bucket HEAD failed (%v) — some bucket-scoped keys can't do this; continuing", err)
	} else {
		pass("bucket reachable")
	}

	origin := ""
	if len(cfg.AllowedOrigins) > 0 {
		origin = cfg.AllowedOrigins[0]
	}

	// Show what the bucket currently allows (so a CORS failure is diagnosable).
	if rules, err := sc.GetBucketCORS(ctx); err != nil {
		info("could not read the bucket's CORS rules (%v)", err)
	} else if len(rules) == 0 {
		info("bucket CORS rules: none")
	} else {
		for i, r := range rules {
			info("bucket CORS rule %d: origins=%v methods=%v headers=%v", i+1, r.Origins, r.Methods, r.Headers)
		}
	}
	if setCORS && origin != "" {
		if err := sc.SetBucketCORS(ctx, cfg.AllowedOrigins); err != nil {
			fail("could not set CORS through the S3 API (%v) — set it in the Backblaze console instead", err)
		} else {
			pass("bucket CORS rules replaced: %v may GET/PUT/HEAD", cfg.AllowedOrigins)
			time.Sleep(3 * time.Second) // let the rule propagate
		}
	}
	prefixCode := "PREFLGHT"
	var okModes []bool
	for _, sign := range []bool{true, false} {
		label := "signed size ON "
		if !sign {
			label = "signed size OFF"
		}
		fmt.Printf("  — upload/download with %s (SIGN_CONTENT_LENGTH=%v)\n", label, sign)
		if testRoundTrip(ctx, sc, prefixCode, sign, origin) {
			okModes = append(okModes, sign)
		}
	}

	switch {
	case len(okModes) == 2:
		pass("both modes work — keep SIGN_CONTENT_LENGTH at its default (true)")
	case len(okModes) == 1 && okModes[0]:
		pass("use SIGN_CONTENT_LENGTH=true (the default)")
	case len(okModes) == 1 && !okModes[0]:
		warn("only unsigned uploads work — set SIGN_CONTENT_LENGTH=false")
	default:
		fail("uploads failed in both modes — see the messages above (usually credentials or CORS)")
	}

	if n, err := sc.CountObjectVersions(ctx, "sessions/"+prefixCode+"/"); err == nil && n > 0 {
		warn("%d old/hidden version(s) remain right after deleting (a store that keeps versions).", n)
		info("With the lifecycle rule \"Keep only the last version\" Backblaze removes them after about a day;")
		info("without it they stay forever and count toward your free quota.")
	} else if err == nil {
		pass("deleted objects leave no stored versions behind")
	}
}

// testRoundTrip presigns, uploads, verifies, downloads, checks CORS and deletes.
func testRoundTrip(ctx context.Context, sc *storage.Client, code string, sign bool, origin string) bool {
	payload := make([]byte, 2048)
	_, _ = rand.Read(payload)
	id := randHex(6)

	url, key, err := sc.PresignPutURL(ctx, code, id, "preflight-name", "application/octet-stream", int64(len(payload)), sign, 2*time.Minute)
	if err != nil {
		fail("could not presign: %v", err)
		return false
	}
	defer sc.DeleteObject(context.Background(), key)

	// CORS preflight, exactly as a browser would send it before a cross-origin PUT.
	if origin != "" {
		checkCORS(url, origin)
	}

	put := func(body []byte) (int, string) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/octet-stream")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err.Error()
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return res.StatusCode, strings.TrimSpace(string(b))
	}
	if code, msg := put(payload); code < 200 || code > 299 {
		fail("upload rejected (HTTP %d): %s", code, msg)
		return false
	}
	pass("upload accepted")

	size, err := sc.ObjectSize(ctx, key)
	switch {
	case err != nil:
		fail("could not read the object back (HEAD): %v", err)
		return false
	case size != int64(len(payload)):
		fail("stored size %d != uploaded %d", size, len(payload))
		return false
	default:
		pass("stored size verified (this is what the server checks at completion)")
	}

	if sign {
		if code, _ := put(append(payload, make([]byte, 10)...)); code >= 200 && code <= 299 {
			info("storage did not enforce the signed size on a mismatched upload; the server's completion check still deletes it")
		} else {
			pass("storage itself rejects an upload of the wrong size")
		}
	}

	gurl, err := sc.PresignGetURL(ctx, key, time.Minute)
	if err != nil {
		fail("could not presign download: %v", err)
		return false
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, gurl, nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		fail("download failed: %v", err)
		return false
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	switch {
	case res.StatusCode != 200 || !bytes.Equal(body, payload):
		fail("download returned HTTP %d / wrong bytes", res.StatusCode)
		return false
	default:
		pass("download returns the same bytes")
	}
	if cd := res.Header.Get("Content-Disposition"); strings.HasPrefix(strings.ToLower(cd), "attachment") {
		pass("downloads are forced to attachment")
	} else {
		warn("download didn't carry Content-Disposition: attachment (got %q)", cd)
	}
	if origin != "" {
		if acao := res.Header.Get("Access-Control-Allow-Origin"); acao == origin || acao == "*" {
			pass("browser downloads from %s are allowed by CORS", origin)
		} else {
			fail("download response has no CORS header for %s — browsers will block downloads", origin)
		}
	}

	if err := sc.DeleteObject(ctx, key); err != nil {
		fail("delete failed: %v", err)
		return false
	}
	if _, err := sc.ObjectSize(ctx, key); err == nil {
		fail("object still readable after delete")
		return false
	}
	pass("delete removes it")
	return true
}

func checkCORS(url, origin string) {
	do := func(o string) *http.Response {
		req, _ := http.NewRequest(http.MethodOptions, url, nil)
		req.Header.Set("Origin", o)
		req.Header.Set("Access-Control-Request-Method", "PUT")
		req.Header.Set("Access-Control-Request-Headers", "content-type")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil
		}
		res.Body.Close()
		return res
	}
	res := do(origin)
	if res == nil {
		fail("CORS preflight request failed")
		return
	}
	acao := res.Header.Get("Access-Control-Allow-Origin")
	if acao != origin && acao != "*" {
		fail("CORS: %s is NOT allowed to upload (HTTP %d, Allow-Origin=%q). Add it in the bucket's CORS rules.", origin, res.StatusCode, acao)
		return
	}
	pass("CORS allows uploads from %s", origin)
	if acao == "*" {
		warn("CORS allows every origin; restrict it to %s", origin)
	}
	if bad := do("https://evil.example"); bad != nil {
		if a := bad.Header.Get("Access-Control-Allow-Origin"); a == "https://evil.example" {
			warn("CORS also allows an unrelated origin")
		}
	}
}
