package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gabriel/invctl/internal/auth"
	"github.com/gabriel/invctl/internal/web/middleware"
	"github.com/gabriel/invctl/internal/web/render"
)

type loginPage struct {
	Base
	Error    string
	Username string
	Next     string
}

// LoginForm renders the sign-in page.
func (a *App) LoginForm(w http.ResponseWriter, r *http.Request) {
	if middleware.UserFrom(r.Context()) != nil {
		render.Redirect(w, r, "/")
		return
	}
	a.Render.Page(w, http.StatusOK, "login", loginPage{
		Base: a.base(r, "Sign in", ""),
		Next: safeNext(r.URL.Query().Get("next")),
	})
}

// Login authenticates and starts a session.
func (a *App) Login(w http.ResponseWriter, r *http.Request) {
	username := formValue(r, "username")
	password := r.PostFormValue("password") // never trimmed: spaces are legitimate
	next := safeNext(formValue(r, "next"))

	user, err := a.Auth.Authenticate(r.Context(), username, password)
	if err != nil {
		if !errors.Is(err, auth.ErrInvalidCredentials) {
			// An operational failure (directory unreachable, database down)
			// is logged in full but still shown to the user as a generic
			// failure -- the detail would only help an attacker.
			slog.Error("authentication failed", "error", err, "username", username)
		} else {
			slog.Warn("failed sign-in", "username", username, "remote", r.RemoteAddr)
		}
		a.Render.Page(w, http.StatusUnauthorized, "login", loginPage{
			Base:     a.base(r, "Sign in", ""),
			Error:    "That username and password combination was not recognised.",
			Username: username,
			Next:     next,
		})
		return
	}

	// A new session id on privilege change defeats session fixation: an
	// attacker who planted a session id cannot ride it into an authenticated
	// session.
	if err := a.Sessions.RenewToken(r.Context()); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Sessions.Put(r.Context(), middleware.SessionUserIDKey, user.Username)
	slog.Info("signed in", "username", user.Username, "source", user.Source,
		"can_write", a.Authz.CanWrite(user))

	if next == "" {
		next = "/"
	}
	render.Redirect(w, r, next)
}

// Logout ends the session.
func (a *App) Logout(w http.ResponseWriter, r *http.Request) {
	if err := a.Sessions.Destroy(r.Context()); err != nil {
		a.serverError(w, r, err)
		return
	}
	render.Redirect(w, r, "/login")
}

// safeNext restricts post-login redirects to paths inside this application.
//
// Without this, /login?next=https://evil.example becomes an open redirect that
// lends this site's credibility to a phishing page. A protocol-relative URL
// (//evil.example) is the case people forget.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return ""
	}
	if strings.Contains(next, "\\") || strings.Contains(next, "\n") || strings.Contains(next, "\r") {
		return ""
	}
	return next
}
