package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/weftbase/weftbase/authz"
	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/internal/httpx"
	"github.com/weftbase/weftbase/internal/reqlog"
)

// Console sign-in (plan §7.4). POST /v1/auth/sessions exchanges a
// one-time link token (printed by `weftbase dev`) for the wb_session
// cookie: HttpOnly, SameSite=Strict, Path=/, Secure except on a
// loopback Host. The session lasts 12 h from its last use (it slides:
// a request more than an hour after the last renewal renews it and
// sets the cookie again), at most 30 days from the sign-in.
// DELETE /v1/auth/sessions/current ends it, with its family (the
// sessions signed in with links it minted, and theirs: authz.Sessions).
// POST /v1/auth/links mints a new link on a running node (`weftbase
// signin`); minted by a signed-in session, the link joins its family.
// DELETE /v1/auth/sessions ends every Console session of the project
// and env (`weftbase signout --all`). Sign-in and sign-out are audited
// (console.session_created, console.session_ended).

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
	setSessionCookie(w, r, id, exp)
	httpx.WriteJSON(w, r, http.StatusCreated, sessionDoc{Operator: operatorName, ExpiresAt: exp})
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, id string, exp time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: authz.SessionCookie, Value: id, Path: "/", Expires: exp,
		MaxAge: int(time.Until(exp).Seconds()), HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: !isLoopbackName(hostOnly(r.Host)),
	})
}

// renew slides the Console session a request authenticated with, and
// sets its cookie again when its end moved.
func (rt *Router) renew(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(authz.SessionCookie)
	if err != nil || c.Value == "" || rt.sessions == nil {
		return
	}
	if exp, renewed, err := rt.sessions.Renew(r.Context(), c.Value); err == nil && renewed {
		setSessionCookie(w, r, c.Value, exp)
	}
}

// linkBody is POST /v1/auth/links' request: all optional.
type linkBody struct {
	// Origin is where the Console is opened ("http://localhost:3000"
	// for a dev server); default: this request's own scheme and Host.
	Origin string `json:"origin"`
}

// linkDoc is POST /v1/auth/links' answer.
type linkDoc struct {
	// Link is the URL to open: <origin>/#signin=<token>. One use.
	Link      string    `json:"link"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// serveLink is POST /v1/auth/links: a fresh one-time Console sign-in
// link from a running node, for a caller that holds a secret key (or
// is signed in already). Audited (console.link_created).
func (rt *Router) serveLink(w http.ResponseWriter, r *http.Request) {
	var body linkBody
	if b, err := io.ReadAll(io.LimitReader(r.Body, 4096)); err != nil || (len(b) > 0 && json.Unmarshal(b, &body) != nil) {
		writeError(w, r, http.StatusBadRequest, "bad_request", `the body is empty or {"origin": "<where the Console is opened>"}`)
		return
	}
	origin := strings.TrimRight(body.Origin, "/")
	if origin == "" {
		scheme := "https"
		if r.TLS == nil && isLoopbackName(hostOnly(r.Host)) {
			scheme = "http"
		}
		origin = scheme + "://" + r.Host
	} else if u, err := url.Parse(origin); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeError(w, r, http.StatusBadRequest, "bad_request", "origin must be an http(s) URL")
		return
	}
	p, sc, _ := authz.From(r.Context())
	var from string
	if p.Kind == authz.Operator {
		// A session's link joins its family: the session it is traded
		// for keeps the family's sign-in time (its 30-day cap) and ends
		// at the family's sign-out.
		if c, err := r.Cookie(authz.SessionCookie); err == nil {
			from = c.Value
		}
	}
	tok, exp, err := rt.sessions.CreateLinkBy(r.Context(), sc, actorOf(p), from)
	if errors.Is(err, authz.ErrSessionEnded) {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "the Console session has ended: sign in again")
		return
	}
	if err != nil {
		reqlog.Cause(r.Context(), "sign-in link: "+err.Error())
		writeError(w, r, http.StatusInternalServerError, "internal", "the sign-in link could not be recorded")
		return
	}
	httpx.WriteJSON(w, r, http.StatusCreated, linkDoc{Link: origin + "/#signin=" + url.QueryEscape(tok), Token: tok, ExpiresAt: exp})
}

func (rt *Router) serveSignOut(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(authz.SessionCookie)
	if err != nil || c.Value == "" {
		writeError(w, r, http.StatusBadRequest, "bad_request", "no Console session cookie on this request")
		return
	}
	p, _, _ := authz.From(r.Context())
	if _, err := rt.sessions.End(r.Context(), c.Value, actorOf(p)); err != nil {
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

// actorOf is the audit actor of a principal.
func actorOf(p authz.Principal) events.Actor {
	return events.Actor{Kind: string(p.Kind), ID: p.ID, Credential: p.Credential, Assurance: p.Assurance}
}

// endAllDoc is DELETE /v1/auth/sessions' answer.
type endAllDoc struct {
	SessionsEnded int `json:"sessions_ended"`
}

// serveEndAll is DELETE /v1/auth/sessions: every Console session of the
// caller's project and env ends, and every unused sign-in link with
// them (a leaked cookie or link, an operator leaving). Audited
// (console.session_ended, reason end_all). A caller signed in with a
// cookie ends its own session too; its cookie is cleared.
func (rt *Router) serveEndAll(w http.ResponseWriter, r *http.Request) {
	p, sc, _ := authz.From(r.Context())
	n, err := rt.sessions.EndAll(r.Context(), sc, actorOf(p))
	if err != nil {
		reqlog.Cause(r.Context(), "end all sessions: "+err.Error())
		writeError(w, r, http.StatusInternalServerError, "internal", "the sessions could not be ended")
		return
	}
	if p.Kind == authz.Operator {
		http.SetCookie(w, &http.Cookie{
			Name: authz.SessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
			SameSite: http.SameSiteStrictMode, Secure: !isLoopbackName(hostOnly(r.Host)),
		})
	}
	httpx.WriteJSON(w, r, http.StatusOK, endAllDoc{SessionsEnded: n})
}
