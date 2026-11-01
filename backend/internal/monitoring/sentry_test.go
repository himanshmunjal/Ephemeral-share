package monitoring

import (
	"strings"
	"testing"

	"github.com/getsentry/sentry-go"
)

func TestScrubRemovesAccessCodes(t *testing.T) {
	cases := map[string]string{
		"GET /api/v1/sessions/K7QX3M9A/content failed":                "sessions/:code",
		"operation error S3: key sessions/K7QX3M9A/abc/def not found": "sessions/:code",
		"redis: session:K7QX3M9A:content timeout":                     "session:*",
		"dial ws:K7QX3M9A refused":                                    "ws:*",
		"ws://x/ws/sessions/K7QX3M9A?who=AbCdEfGhIjKlMnOp":            "who=REDACTED",
	}
	for in, want := range cases {
		out := Scrub(in)
		if strings.Contains(out, "K7QX3M9A") || strings.Contains(out, "AbCdEfGhIjKlMnOp") {
			t.Errorf("secret survived scrubbing: %q -> %q", in, out)
		}
		if !strings.Contains(out, want) {
			t.Errorf("Scrub(%q) = %q, want it to contain %q", in, out, want)
		}
	}
}

func TestBeforeSendStripsRequestAndSecrets(t *testing.T) {
	ev := &sentry.Event{
		Message:     "failed for sessions/K7QX3M9A",
		Request:     &sentry.Request{URL: "https://api.example/api/v1/sessions/K7QX3M9A"},
		User:        sentry.User{IPAddress: "1.2.3.4"},
		Exception:   []sentry.Exception{{Value: "head object sessions/K7QX3M9A/x: 404"}},
		Tags:        map[string]string{"route": "/api/v1/sessions/:code", "raw": "sessions/K7QX3M9A"},
		Contexts:    map[string]sentry.Context{"log": {"error": "session:K7QX3M9A gone"}},
		Transaction: "GET /sessions/K7QX3M9A",
	}
	got := beforeSend(ev, nil)
	if got.Request != nil || got.User.IPAddress != "" {
		t.Error("request data / user info must be dropped")
	}
	blob := got.Message + got.Exception[0].Value + got.Tags["raw"] + got.Transaction + got.Contexts["log"]["error"].(string)
	if strings.Contains(blob, "K7QX3M9A") {
		t.Errorf("access code reached the outgoing event: %s", blob)
	}
}

func TestInitWithoutDSNIsANoop(t *testing.T) {
	enabled, flush := Init("", "test")
	if enabled {
		t.Error("must be disabled without a DSN")
	}
	flush()
}
