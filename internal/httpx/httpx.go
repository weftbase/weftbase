// Package httpx is the server's small HTTP plumbing: the JSON writer
// and the error shape every JSON route answers with
// ({"error":{code,message,request_id?}}).
package httpx

import (
	"encoding/json"
	"net/http"

	"github.com/weftbase/weftbase/internal/reqlog"
)

// WriteJSON writes v as the route's JSON body (no body on HEAD).
func WriteJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError answers in the error shape. The reason is the request's
// in the server log (internal/reqlog): a 5xx is one ERROR line naming
// it and the request id, which a 5xx body carries too (request_id), so
// a user's report meets its line in the server's log.
func WriteError(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	reqlog.Refuse(r.Context(), code, msg)
	type body struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id,omitempty"`
	}
	b := body{Code: code, Message: msg}
	if status >= 500 {
		b.RequestID = reqlog.ID(r.Context())
	}
	WriteJSON(w, r, status, struct {
		Error body `json:"error"`
	}{b})
}
