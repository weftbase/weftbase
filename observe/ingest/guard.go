package ingest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/weftbase/weftbase/obsdb"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// AttrIngestKey is the resource attribute a guarded batch's rows carry:
// the id of the API key that sent them.
const AttrIngestKey = "weftbase.ingest.key"

// Gate restricts what a caller may ingest. A worker key minted for one
// build may write the records and spans of the runs dispatched to that
// build (and of their subagent children) only: one leaked key, or one
// misbehaving build, cannot forge another run's record.
type Gate interface {
	// Restricted reports whether r's caller is limited to some runs, and
	// the key id its rows are tagged with. An unrestricted caller's
	// batch is not read here.
	Restricted(r *http.Request) (keyID string, restricted bool)
	// Refused returns those of runIDs r's caller may not write.
	Refused(ctx context.Context, r *http.Request, runIDs []string) ([]string, error)
}

// Guard wraps an OTLP receiver (Traces or Logs; logs when logs is true)
// with gate. A restricted caller's batch is decoded, every run it names
// is checked, and a batch naming any run the caller may not write is
// refused whole (403 forbidden: the exporter does not retry it). An
// admitted batch is tagged (AttrIngestKey on every resource) and handed
// on. Spans and records that name no run pass: they cannot write a
// run's record.
func Guard(next http.Handler, gate Gate, logs bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keyID, restricted := gate.Restricted(r)
		if !restricted {
			next.ServeHTTP(w, r)
			return
		}
		var msg proto.Message
		if logs {
			msg = new(collogspb.ExportLogsServiceRequest)
		} else {
			msg = new(coltracepb.ExportTraceServiceRequest)
		}
		if !decode(w, r, msg) {
			return
		}
		var runs []string
		add := func(id string) {
			if id != "" && !slices.Contains(runs, id) {
				runs = append(runs, id)
			}
		}
		switch m := msg.(type) {
		case *coltracepb.ExportTraceServiceRequest:
			for _, s := range obsdb.FromOTLPTraces(m) {
				d := obsdb.DeriveSpan(s)
				add(d.RunID)
				add(d.ParentRunID)
			}
			for _, rs := range m.ResourceSpans {
				rs.Resource = tag(rs.Resource, keyID)
			}
		case *collogspb.ExportLogsServiceRequest:
			for _, rec := range obsdb.FromOTLPLogs(m) {
				d := obsdb.DeriveRecord(rec)
				add(d.RunID)
				add(d.ParentRunID)
			}
			for _, rl := range m.ResourceLogs {
				rl.Resource = tag(rl.Resource, keyID)
			}
		}
		if len(runs) > 0 {
			refused, err := gate.Refused(r.Context(), r, runs)
			if err != nil {
				writeIngestError(w, r, http.StatusServiceUnavailable, "unavailable", "ownership check: "+err.Error())
				return
			}
			if len(refused) > 0 {
				writeIngestError(w, r, http.StatusForbidden, "forbidden",
					fmt.Sprintf("this key writes only the runs dispatched to its build; the batch names %s", listRuns(refused)))
				return
			}
		}
		// Hand the batch on in the format it came in, decompressed.
		ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		var b []byte
		var err error
		if ct == "application/json" {
			b, err = protojson.Marshal(msg)
		} else {
			b, err = proto.Marshal(msg)
		}
		if err != nil {
			writeIngestError(w, r, http.StatusInternalServerError, "internal", "re-encode: "+err.Error())
			return
		}
		r2 := r.Clone(r.Context())
		r2.Body = io.NopCloser(bytes.NewReader(b))
		r2.ContentLength = int64(len(b))
		r2.Header.Del("Content-Encoding")
		r2.Header.Set("Content-Length", strconv.Itoa(len(b)))
		next.ServeHTTP(w, r2)
	})
}

// tag sets AttrIngestKey on res (over any value the sender put there).
func tag(res *resourcepb.Resource, keyID string) *resourcepb.Resource {
	if res == nil {
		res = &resourcepb.Resource{}
	}
	res.Attributes = slices.DeleteFunc(res.Attributes, func(kv *commonpb.KeyValue) bool { return kv.GetKey() == AttrIngestKey })
	res.Attributes = append(res.Attributes, &commonpb.KeyValue{Key: AttrIngestKey,
		Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: keyID}}})
	return res
}

// listRuns names at most three run ids.
func listRuns(ids []string) string {
	q := make([]string, 0, 3)
	for _, id := range ids[:min(len(ids), 3)] {
		q = append(q, capEcho(strconv.Quote(id)))
	}
	s := "run " + strings.Join(q, ", ")
	if len(ids) > 3 {
		s += fmt.Sprintf(" and %d more", len(ids)-3)
	}
	return s
}
