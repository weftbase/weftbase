package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/weftbase/weftbase/authz"
	"github.com/weftbase/weftbase/internal/httpx"
	"github.com/weftbase/weftbase/internal/reqlog"
)

// Console sign-in (plan §7.4). POST /v1/auth/sessions exchanges a
// one-time link token (printed by `weftbase dev`) for the wb_session
// cookie: HttpOnly, SameSite=Strict, Path=/, Secure except on a
// loopback Host, 12 h. DELETE /v1/auth/sessions/current ends it.

// signInBody is POST /v1/auth/sessions' request.
type signInBody struct {
	Token string `json:"token"`
}

// sessionDoc is POST /v1/auth/sessions' answer.
type sessionDoc struct {
	Operator  string    `json:"operator"`
	ExpiresAt time.Time `json:"expires_at"`
}

// operatorName is the operator a link signs in until members exist.
const operatorName = "operator"

func (rt *Router) serveSignIn(w http.ResponseWriter, r *http.Request) {
	if crossOriginWrite(rt.origins, r) {
		writeError(w, r, http.StatusForbidden, "forbidden", crossOriginMsg(r))
		return
	}
	var body signInBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil || body.Token == "" {
		writeError(w, r, http.StatusBadRequest, "bad_request", `the body must be {"token": "<sign-in link token>"}`)
		return
	}
	id, exp, err := rt.sessions.Exchange(r.Context(), body.Token, operatorName)
	switch {
	case errors.Is(err, authz.ErrLinkUsed):
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "the sign-in link is unknown, used or expired: ask for a new one")
		return
	case err != nil:
		reqlog.Cause(r.Context(), "sign in: "+err.Error())
		writeError(w, r, http.StatusInternalServerError, "internal", "the sign-in could not be recorded")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: authz.SessionCookie, Value: id, Path: "/", Expires: exp,
		MaxAge: int(time.Until(exp).Seconds()), HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: !isLoopbackName(hostOnly(r.Host)),
	})
	httpx.WriteJSON(w, r, http.StatusCreated, sessionDoc{Operator: operatorName, ExpiresAt: exp})
}

func (rt *Router) serveSignOut(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(authz.SessionCookie)
	if err != nil || c.Value == "" {
		writeError(w, r, http.StatusBadRequest, "bad_request", "no Console session cookie on this request")
		return
	}
	if err := rt.sessions.End(r.Context(), c.Value); err != nil {
		reqlog.Cause(r.Context(), "sign out: "+err.Error())
		writeError(w, r, http.StatusInternalServerError, "internal", "the session could not be ended")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: authz.SessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: !isLoopbackName(hostOnly(r.Host)),
	})
	w.WriteHeader(http.StatusNoContent)
}
