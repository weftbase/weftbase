package exporter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"sync"

	"github.com/weftbase/weft/otel"
	"github.com/weftbase/weftbase/internal/discovery"
	"github.com/weftbase/weftbase/obsdb"
)

// envGetenv is the package's one environment seam (tests swap it).
var envGetenv = os.Getenv

type markerKind int

const (
	kindLocal markerKind = iota + 1
	kindStudio
	kindNoDiscovery
)

// marker is what this package knows of an option it handed out: each
// Local, Studio and NoDiscovery option is an otel.Tag carrying its
// marker, so Install and Start recognise them among the options they
// are given (otel.TagOf), and any pipeline that built one of their
// destinations lists it (Pipeline.Tags) — a plain otel.Install
// included. Nothing is registered: a marker lives as long as its
// option.
type marker struct {
	kind       markerKind
	url, token string       // Studio
	handle     *localHandle // Local: its open database, under handleMu
}

// handleMu guards every marker's handle.
var handleMu sync.Mutex

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
// WEFTBASE_DISCOVERY=off. A pipeline that names a Studio or a Local needs
// no NoDiscovery: either turns the read off on its own.
func NoDiscovery() otel.Option {
	return otel.Tag(&marker{kind: kindNoDiscovery})
}

// Start is otel.Start with the platform's environment: WEFTBASE_URL
// and WEFTBASE_TOKEN add a Studio destination (content on), and,
// when they are unset and the options name no Local and no Studio,
// the discovery file of a running `weftbase dev` stands in
// for them (unless NoDiscovery, otel.NoEnv or WEFTBASE_DISCOVERY=off). It
// adds no fallback: with nothing configured it returns
// otel.ErrNoDestinations.
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
// zero-config rule: with no destination configured (in code, the
// OTEL_* variables, WEFTBASE_URL or the discovery file) it writes
// Local("") — $WEFTBASE_DB or ./.weftbase/weftbase.db — and so it does, with one
// WARN, when only environment destinations were configured and none
// could be built. Like otel.Install it never fails the program; the
// returned function flushes and shuts everything down.
func Install(opts ...otel.Option) func() {
	all, joined := plan(opts, true)
	stop := otel.Install(all...)
	// The INFO line only for the pipeline this call built: the
	// environment Studio's marker is this call's own, so a previous
	// pipeline otel.Installed still reports (this Install recorded
	// nothing) never carries it.
	joined.log(otel.Installed())
	return stop
}

// plan adds the platform's environment destinations (and, for Install,
// the Local fallback) to opts. The environment's Studio goes in through
// otel.Environment: otel drops it when a destination named in code
// exports to the same URL (an exporter.Studio or a plain otel.OTLP —
// the explicit one wins), and it never counts as named in code, so
// Install's fallback still takes over when it fails to build.
func plan(opts []otel.Option, fallback bool) (all []otel.Option, joined *discovered) {
	noEnv, noDisc, named := false, false, false
	for _, o := range opts {
		if o == otel.NoEnv() {
			noEnv = true
		}
		m := find(o)
		if m == nil {
			continue
		}
		switch m.kind {
		case kindLocal, kindStudio:
			named = true
		case kindNoDiscovery:
			noDisc = true
		}
	}
	all = append([]otel.Option(nil), opts...)
	if !noEnv {
		getenv := envGetenv
		// Explicit > environment > file: a Studio or a Local named in
		// code switches the discovery read off, as WEFTBASE_URL and
		// NoDiscovery do — the file never adds a second Studio, never
		// writes runs into a store the caller did not name, and never
		// becomes the one StudioEndpoint (and runtime.Install) reports.
		if !noDisc && !named {
			getenv, joined = discoverStudio(getenv)
		}
		if u := getenv("WEFTBASE_URL"); u != "" {
			u = normalizeEndpoint(u)
			if err := envStudioURL(u); err != nil {
				// Refused here rather than by otel: the WARN names the
				// variable, and Install keeps its Local fallback.
				slog.Warn("weft/exporter: WEFTBASE_URL skipped", "err", err.Error())
				joined = nil
			} else {
				s := Studio(u, getenv("WEFTBASE_TOKEN"), otel.Name("studio(env)"))
				all = append(all, otel.Environment(s))
				if joined != nil {
					joined.m = find(s)
				}
			}
		} else {
			joined = nil
		}
	}
	if fallback {
		all = append(all, otel.Fallback(Local("")))
	}
	return all, joined
}

// LocalDB returns the installed pipeline's Local database — for
// studio.DB(exporter.LocalDB()), the handle setup A shares with Studio:
// LocalDBOf(otel.Installed()). nil before an install, after its
// shutdown, or without a Local.
func LocalDB() obsdb.DB {
	p := otel.Installed()
	if p == nil {
		return nil
	}
	return LocalDBOf(p)
}

// LocalDBOf returns the database of p's Local destination (the last one
// configured that is open), else nil — for any pipeline: Install,
// Start (otel.NoGlobal included) or a plain otel.Install/otel.Start
// given an exporter.Local.
func LocalDBOf(p *otel.Pipeline) obsdb.DB {
	ms := markersOf(p)
	handleMu.Lock()
	defer handleMu.Unlock()
	for i := len(ms) - 1; i >= 0; i-- {
		if ms[i].kind != kindLocal {
			continue
		}
		if h := ms[i].handle; h != nil && h.isOpen() {
			return h.db
		}
	}
	return nil
}

// StudioEndpoint returns the installed pipeline's Studio destination
// (url and token), which weftbase/runtime dials by default:
// StudioEndpointOf(otel.Installed()). "" before an install, after its
// shutdown, and for a pipeline without a Studio.
func StudioEndpoint() (string, string) {
	p := otel.Installed()
	if p == nil {
		return "", ""
	}
	return StudioEndpointOf(p)
}

// StudioEndpointOf returns the Studio destination p built, installed or
// not: the environment's (WEFTBASE_URL or the discovery file) when it
// added one, else the last exporter.Studio configured. Any pipeline
// with an exporter.Studio destination counts — one a plain otel.Install
// built included; a Studio Install skipped (it failed to build) or the
// environment's dropped as a duplicate does not.
func StudioEndpointOf(p *otel.Pipeline) (string, string) {
	ms := markersOf(p)
	for i := len(ms) - 1; i >= 0; i-- {
		if ms[i].kind == kindStudio {
			return ms[i].url, ms[i].token
		}
	}
	return "", ""
}

// discovered is the running Studio the discovery file named, and the
// marker of the environment Studio built from it.
type discovered struct {
	info discovery.Info
	path string
	m    *marker
}

// log is the one INFO line — which Studio this process joined, and how
// to stop it doing so — when p built the Studio the file named.
func (d *discovered) log(p *otel.Pipeline) {
	if d == nil || d.m == nil {
		return
	}
	for _, m := range markersOf(p) {
		if m == d.m {
			slog.Info("weft/exporter: joined the running Studio", "url", d.info.URL,
				"pid", d.info.PID, "file", d.path,
				"hint", "WEFTBASE_URL names another; exporter.NoDiscovery() or WEFTBASE_DISCOVERY=off ignores the file")
			return
		}
	}
}

// discoverStudio is the discovery rung: when WEFTBASE_URL is unset
// and WEFTBASE_DISCOVERY is not "off", the discovery file `weftbase dev` /
// `weftbase dev` writes (internal/discovery: ./.weftbase, then
// $XDG_RUNTIME_DIR/weftbase, then the user cache directory) stands in for
// WEFTBASE_URL and WEFTBASE_TOKEN — its url and token, together.
// A stale file (its pid gone, or 24 hours old) is never trusted; a
// missing, stale or malformed one changes nothing and logs Debug at
// most.
func discoverStudio(getenv func(string) string) (func(string) string, *discovered) {
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

// envStudioURL is the check otel applies to an OTLP endpoint, for the
// environment's Studio: an http(s) URL with a host, http:// only to a
// loopback host — WEFTBASE_URL carries a bearer token, so plaintext
// to another host takes Studio(url, token, otel.Insecure()) in code.
// The errors never echo the raw value: a URL may carry credentials.
func envStudioURL(raw string) error {
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
		return fmt.Errorf("http:// to non-loopback %q needs otel.Insecure() on a Studio named in code", u.Host)
	}
	return fmt.Errorf("endpoint scheme %q: OTLP/HTTP takes http:// or https://", u.Scheme)
}
