package exporter

import (
	"strings"
	"time"

	"github.com/weftbase/weft/otel"
)

// Studio's batch defaults: records reach a live viewer within 200 ms,
// spans within 1 s, and a dev loop's event storm (thousands of deltas
// and events in milliseconds) fits the queue.
const (
	studioLogDelay  = 200 * time.Millisecond
	studioSpanDelay = time.Second
	studioQueueSize = 16384
)

// Studio exports OTLP/HTTP protobuf to a Weft Studio with a bearer
// token: otel.OTLP marked otel.FirstParty (content on, raw bodies and
// app logs eligible), traces + logs, short batches (logs 200 ms, spans
// 1 s) and a 16,384-record queue; opts override any of them. http:// to
// a non-loopback host needs otel.Insecure(). Naming a Studio switches
// the discovery file off in Install and Start (as naming a Local does),
// and makes it the endpoint StudioEndpoint reports for any pipeline
// that built it, a plain otel.Install's included (WEFTBASE_URL still
// adds its own under Install and Start, de-duplicated by URL).
func Studio(url, token string, opts ...otel.DestOption) otel.Option {
	m := &marker{kind: kindStudio, url: normalizeEndpoint(url), token: token}
	base := []otel.DestOption{
		otel.Name("studio"), otel.FirstParty(),
		otel.BatchDelays(studioLogDelay, studioSpanDelay), otel.QueueSize(studioQueueSize),
	}
	if token != "" {
		base = append(base, otel.Headers(map[string]string{"Authorization": "Bearer " + token}))
	}
	return otel.Tag(m, otel.OTLP(url, append(base, opts...)...))
}

// normalizeEndpoint applies the OTLP endpoint convention otel applies:
// a value without a scheme is https://.
func normalizeEndpoint(u string) string {
	if u == "" || strings.Contains(u, "://") {
		return u
	}
	return "https://" + u
}
