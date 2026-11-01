// Package monitoring wires up optional Sentry error tracking.
//
// The access code in a session URL is also the encryption key, so it must
// never reach a third-party service. Every event is scrubbed before sending,
// request data is dropped entirely, and PII sending is off.
package monitoring

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/getsentry/sentry-go"
)

var (
	// Anything that looks like it carries a session code: URL paths, Redis
	// keys, S3 object keys (which AWS SDK errors like to echo back).
	codePatterns = []*regexp.Regexp{
		regexp.MustCompile(`sessions/[^/\s"'?&#]+`),
		regexp.MustCompile(`session:[A-Za-z0-9]+`),
		regexp.MustCompile(`ws:[A-Za-z0-9]+`),
		regexp.MustCompile(`(?i)([?&](?:who|code)=)[^&\s"']+`),
	}
	codeReplacements = []string{"sessions/:code", "session:*", "ws:*", "${1}REDACTED"}
)

// Scrub removes session codes from a string.
func Scrub(s string) string {
	for i, re := range codePatterns {
		s = re.ReplaceAllString(s, codeReplacements[i])
	}
	return s
}

func scrubMap(m map[string]interface{}) {
	for k, v := range m {
		switch t := v.(type) {
		case string:
			m[k] = Scrub(t)
		case map[string]interface{}:
			scrubMap(t)
		}
	}
}

func beforeSend(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	event.Request = nil // URLs, headers, cookies: all of it
	event.User = sentry.User{}
	event.ServerName = ""
	event.Message = Scrub(event.Message)
	for i := range event.Exception {
		event.Exception[i].Value = Scrub(event.Exception[i].Value)
	}
	event.Transaction = Scrub(event.Transaction)
	for k, v := range event.Tags {
		event.Tags[k] = Scrub(v)
	}
	for _, c := range event.Contexts {
		scrubMap(c)
	}
	for _, b := range event.Breadcrumbs {
		b.Message = Scrub(b.Message)
		scrubMap(b.Data)
	}
	return event
}

// Init enables Sentry when a DSN is configured. It returns whether it did,
// and a flush function to call on shutdown.
func Init(dsn, environment string) (enabled bool, flush func()) {
	if dsn == "" {
		return false, func() {}
	}
	err := sentry.Init(sentry.ClientOptions{
		Dsn:              dsn,
		Environment:      environment,
		SendDefaultPII:   false,
		AttachStacktrace: true,
		TracesSampleRate: 0, // errors only; keeps us far inside the free quota
		BeforeSend:       beforeSend,
	})
	if err != nil {
		slog.Warn("sentry init failed; continuing without error tracking", "error", err)
		return false, func() {}
	}
	return true, func() { sentry.Flush(2 * time.Second) }
}

// CapturePanic reports a recovered panic, tagged with the route pattern.
func CapturePanic(route string, recovered any) {
	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetTag("route", route)
		sentry.CaptureException(fmt.Errorf("panic: %v", recovered))
	})
}

// handler forwards Error-level log records to Sentry and passes everything on
// to the wrapped handler unchanged.
type handler struct{ next slog.Handler }

// Wrap returns a slog handler that also reports Error-level records to Sentry.
func Wrap(next slog.Handler) slog.Handler { return &handler{next: next} }

func (h *handler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelError {
		sentry.WithScope(func(scope *sentry.Scope) {
			var route string
			details := sentry.Context{}
			r.Attrs(func(a slog.Attr) bool {
				if a.Key == "route" {
					route = a.Value.String()
					scope.SetTag("route", route)
				} else {
					details[a.Key] = Scrub(a.Value.String())
				}
				return true
			})
			scope.SetContext("log", details)
			scope.SetFingerprint([]string{r.Message, route})
			sentry.CaptureMessage(Scrub(r.Message))
		})
	}
	return h.next.Handle(ctx, r)
}

func (h *handler) WithAttrs(a []slog.Attr) slog.Handler { return &handler{next: h.next.WithAttrs(a)} }
func (h *handler) WithGroup(n string) slog.Handler      { return &handler{next: h.next.WithGroup(n)} }
