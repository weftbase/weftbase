// Package release is weftbase's own version. weftbase has its own
// version line (it started at v0.1.0) and
// reports the weft version it embeds beside it: that one is weft's
// version package (github.com/weftbase/weft/version), which GET
// /v1/meta reports beside it — the apps it talks to speak weft's
// records.
package release

// Version is the weftbase module's release tag. The release process
// bumps it with the tag; `weftbase version` prints it beside weft's.
const Version = "v0.1.0"
