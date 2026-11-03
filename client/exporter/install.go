package exporter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"

	"github.com/weftbase/weft/otel"
	"github.com/weftbase/weftbase/client/discovery"
)

// envGetenv is the package's one environment seam (tests swap it).
var envGetenv = os.Getenv

type markerKind int

const (
	kindNamed markerKind = iota + 1
	kindRemote
	kindNoDiscovery
	kindFallback
)

// marker is what this package knows of an option it handed out: each
// Named, Remote, NoDiscovery and WithFallback option is an otel.Tag
// carrying its marker, so Install and Start recognise them among the
// options they are given (otel.TagOf), and any pipeline that built one
// of their destinations lists it (Pipeline.Tags) — a plain
// otel.Install included. Nothing is registered: a marker lives as long
// as its option.
type marker struct {
	kind     markerKind
	url, key string        // Remote
	fallback []otel.Option // WithFallback
}

// find returns the marker of an option this package handed out, else
// nil.
func find(o otel.Option) *marker {
	m, _ := otel.TagOf(o).(*marker)
	return m
}

// markersOf returns the markers of the destinations p built, in
// destination order.
func markersOf(p *otel.Pipeline) []*marker {
	if p == nil {
		return nil
	}
	var out []*marker
	for _, t := range p.Tags() {
		if m, ok := t.(*marker); ok {
			out = append(out, m)
		}
	}
	return out
}

// NoDiscovery makes Install and Start skip the discovery file a running
// `weftbase dev` wrote, and nothing else: WEFTBASE_URL,
// the OTEL_* variables and the WEFT_ENV development gate are read as
// usual (otel.NoEnv drops them all). The per-pipeline form of
// WEFTBASE_DISCOVERY=off. A pipeline that names a Remote or a Named
// destination needs no NoDiscovery: either turns the read off on its
// own.
func NoDiscovery() otel.Option {
	return otel.Tag(&marker{kind: kindNoDiscovery})
}

// Named marks opts as destinations the caller named in code, the way a
// Remote is: Install and Start then skip the discovery file, so the
// pipeline writes to the stores it names (and the environment's),
// never into a running server it found on disk. An embedded sink built
// outside this module (the server's in-process store) wraps itself in
// Named.
func Named(opts ...otel.Option) otel.Option {
	return otel.Tag(&marker{kind: kindNamed}, opts...)
}

// WithFallback is Install's zero-config rule: with no destination
// configured (in code, the OTEL_* variables, WEFTBASE_URL or the
// discovery file) Install builds opts instead — and so it does, with
// one WARN, when only environment destinations were configured and
// none could be built. Start ignores it. Without one, Install adds no
// fallback. The client module ships no store of its own: the server
// module's embedded sink is the usual one
// (exporter.WithFallback(local.Local(""))).
func WithFallback(opts ...otel.Option) otel.Option {
	return otel.Tag(&marker{kind: kindFallback, fallback: append([]otel.Option(nil), opts...)})
}

// Start is otel.Start with the platform's environment: WEFTBASE_URL
// and WEFTBASE_TOKEN add a Remote destination (content on), and,
// when they are unset and the options name no Remote and no Named
// destination, the discovery file of a running `weftbase dev` stands
// in for them (unless NoDiscovery, otel.NoEnv or
// WEFTBASE_DISCOVERY=off). It adds no fallback: with nothing configured
// it returns otel.ErrNoDestinations.
func Start(ctx context.Context, opts ...otel.Option) (*otel.Pipeline, error) {
	all, joined := plan(opts, false)
	p, err := otel.Start(ctx, all...)
	if err != nil {
		return nil, err
	}
	joined.log(p)
	return p, nil
}

// Install is otel.Install with Start's environment, plus the
// WithFallback rule. Like otel.Install it never fails the program; the
// returned function flushes and shuts everything down.
func Install(opts ...otel.Option) func() {
	all, joined := plan(opts, true)
	stop := otel.Install(all...)
	// The INFO line only for the pipeline this call built: the
	// environment Remote's marker is this call's own, so a previous
	// pipeline otel.Installed still reports (this Install recorded
	// nothing) never carries it.
	joined.log(otel.Installed())
	return stop
}

// plan adds the platform's environment destinations (and, for Install,
// the WithFallback options) to opts. The environment's Remote goes in
// through otel.Environment: otel drops it when a destination named in
// code exports to the same URL (an exporter.Remote or a plain
// otel.OTLP — the explicit one wins), and it never counts as named in
// code, so Install's fallback still takes over when it fails to build.
func plan(opts []otel.Option, fallback bool) (all []otel.Option, joined *discovered) {
	noEnv, noDisc, named := false, false, false
	var fb []otel.Option
	for _, o := range opts {
		if o == otel.NoEnv() {
			noEnv = true
		}
		m := find(o)
		if m == nil {
			continue
		}
		switch m.kind {
		case kindNamed, kindRemote:
			named = true
		case kindNoDiscovery:
			noDisc = true
		case kindFallback:
			fb = append(fb, m.fallback...)
		}
	}
	all = append([]otel.Option(nil), opts...)
	if !noEnv {
		getenv := envGetenv
		// Explicit > environment > file: a Remote or a Named destination
		// in code switches the discovery read off, as WEFTBASE_URL and
		// NoDiscovery do — the file never adds a second Remote and never
		// writes runs into a store the caller did not name.
		if !noDisc && !named {
			getenv, joined = discoverServer(getenv)
		}
		if u := getenv("WEFTBASE_URL"); u != "" {
			u = normalizeEndpoint(u)
			if err := envServerURL(u); err != nil {
				// Refused here rather than by otel: the WARN names the
				// variable, and Install keeps its fallback.
				slog.Warn("weft/exporter: WEFTBASE_URL skipped", "err", err.Error())
				joined = nil
			} else {
				s := Remote(u, getenv("WEFTBASE_TOKEN"), otel.Name("weftbase(env)"))
				all = append(all, otel.Environment(s))
				if joined != nil {
					joined.m = find(s)
				}
			}
		} else {
			joined = nil
		}
	}
	if fallback && len(fb) > 0 {
		all = append(all, otel.Fallback(fb...))
	}
	return all, joined
}

// discovered is the running server the discovery file named, and the
// marker of the environment Remote built from it.
type discovered struct {
	info discovery.Info
	path string
	m    *marker
}

// log is the one INFO line — which server this process joined, and how
// to stop it doing so — when p built the Remote the file named.
func (d *discovered) log(p *otel.Pipeline) {
	if d == nil || d.m == nil {
		return
	}
	for _, m := range markersOf(p) {
		if m == d.m {
			slog.Info("weft/exporter: joined the running weftbase dev", "url", d.info.URL,
				"pid", d.info.PID, "file", d.path,
				"hint", "WEFTBASE_URL names another; exporter.NoDiscovery() or WEFTBASE_DISCOVERY=off ignores the file")
			return
		}
	}
}

// discoverServer is the discovery rung: when WEFTBASE_URL is unset
// and WEFTBASE_DISCOVERY is not "off", the discovery file
// `weftbase dev` writes (client/discovery: ./.weftbase, then
// $XDG_RUNTIME_DIR/weftbase, then the user cache directory) stands in for
// WEFTBASE_URL and WEFTBASE_TOKEN — its url and token, together.
// A stale file (its pid gone, or 24 hours old) is never trusted; a
// missing, stale or malformed one changes nothing and logs Debug at
// most.
func discoverServer(getenv func(string) string) (func(string) string, *discovered) {
	info, path, ok := discovery.Lookup(getenv, slog.Default())
	if !ok {
		return getenv, nil
	}
	return func(k string) string {
		switch k {
		case "WEFTBASE_URL":
			return info.URL
		case "WEFTBASE_TOKEN":
			return info.Token
		}
		return getenv(k)
	}, &discovered{info: info, path: path}
}

// envServerURL is the check otel applies to an OTLP endpoint, for the
// environment's Remote: an http(s) URL with a host, http:// only to a
// loopback host — WEFTBASE_URL carries a bearer key, so plaintext
// to another host takes Remote(url, key, otel.Insecure()) in code.
// The errors never echo the raw value: a URL may carry credentials.
func envServerURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("bad endpoint URL: it does not parse")
	}
	if u.Host == "" {
		return fmt.Errorf("bad endpoint URL %q: no host", u.Redacted())
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		switch u.Hostname() {
		case "localhost", "127.0.0.1", "::1":
			return nil
		}
		return fmt.Errorf("http:// to non-loopback %q needs otel.Insecure() on a Remote named in code", u.Host)
	}
	return fmt.Errorf("endpoint scheme %q: OTLP/HTTP takes http:// or https://", u.Scheme)
}
