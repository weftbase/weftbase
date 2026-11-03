package exporter

import (
	"strings"
	"time"

	"github.com/weftbase/weft/otel"
)

// The remote destination's batch defaults: records reach a live viewer
// within 200 ms, spans within 1 s, and a dev loop's event storm
// (thousands of deltas and events in milliseconds) fits the queue.
const (
	remoteLogDelay  = 200 * time.Millisecond
	remoteSpanDelay = time.Second
	remoteQueueSize = 16384
)

// Remote exports OTLP/HTTP protobuf to a weftbase server at url with an
// ingest key as the bearer: otel.OTLP to <url>/otlp (so <url>/otlp/v1/traces
// and <url>/otlp/v1/logs) marked otel.FirstParty (content on, raw bodies
// and app logs eligible), traces + logs, short batches (logs 200 ms,
// spans 1 s) and a 16,384-record queue; opts override any of them. The
// destination is named "weftbase". http:// to a non-loopback host needs
// otel.Insecure(). Naming a Remote switches the discovery file off in
// Install and Start (as Named does) — WEFTBASE_URL still adds its own
// under Install and Start, de-duplicated by URL.
func Remote(url, key string, opts ...otel.DestOption) otel.Option {
	m := &marker{kind: kindRemote, url: normalizeEndpoint(url), key: key}
	base := []otel.DestOption{
		otel.Name("weftbase"), otel.FirstParty(),
		otel.BatchDelays(remoteLogDelay, remoteSpanDelay), otel.QueueSize(remoteQueueSize),
	}
	if key != "" {
		base = append(base, otel.Headers(map[string]string{"Authorization": "Bearer " + key}))
	}
	return otel.Tag(m, otel.OTLP(otlpEndpoint(url), append(base, opts...)...))
}

// otlpEndpoint is the OTLP base a server URL serves ingest under: the
// SDK appends /v1/traces and /v1/logs to it.
func otlpEndpoint(url string) string {
	if url == "" {
		return url
	}
	return strings.TrimRight(url, "/") + "/otlp"
}

// normalizeEndpoint applies the OTLP endpoint convention otel applies:
// a value without a scheme is https://.
func normalizeEndpoint(u string) string {
	if u == "" || strings.Contains(u, "://") {
		return u
	}
	return "https://" + u
}
