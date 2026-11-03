// Package exporter is an app's side of weftbase's observability: the
// destination that sends weft's traces and records to a weftbase
// server, and the zero-config behaviour a developer expects on top of
// weft/otel.
//
//	defer exporter.Install()() // a running `weftbase dev`, else WEFTBASE_URL
//
//	defer exporter.Install(
//	    exporter.Remote("https://wb.example.com", key), // OTLP with a bearer key, content on
//	    otel.Datadog(),                                 // any weft/otel option beside it
//	)()
//
// weft/otel knows nothing of weftbase: it emits, and reads only the
// standard OpenTelemetry variables. This package builds on its public
// seams — otel.OTLP with otel.FirstParty (Remote), otel.Fallback
// (WithFallback), otel.Environment (WEFTBASE_URL's Remote) and otel.Tag
// (so Install and Start know a Remote or a Named destination among
// their options) — and adds what the platform reads:
//
//	WEFTBASE_URL, WEFTBASE_TOKEN          → a Remote destination (content on)
//	(neither, nor WEFTBASE_DISCOVERY=off) → the discovery file a running `weftbase dev` wrote
//
// The OTLP endpoint is the server URL plus /otlp: traces go to
// <url>/otlp/v1/traces and records to <url>/otlp/v1/logs (a plain OTel
// SDK sets OTEL_EXPORTER_OTLP_ENDPOINT=http://host:7331/otlp).
//
// The discovery file is read only by Install and Start, when their
// options name no Remote and no Named destination, without NoDiscovery
// or otel.NoEnv. Install adds the WithFallback options when nothing
// else is configured.
//
// The package is part of the client module: it imports weft, the
// standard library and OpenTelemetry only. The embedded in-process
// sink is the server's (observe/local).
package exporter
