package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
)

const pbURL = "http://localhost:8090"

type tmplData struct {
	Error   string
	Success string
	Email   string
}

var loginTmpl = template.Must(template.New("login").Parse(`<div id="auth">
  <h2>Login</h2>
  {{if .Error}}<p class="error">{{.Error}}</p>{{end}}
  {{if .Success}}<p class="success">{{.Success}}</p>{{end}}
  <form hx-post="/auth/login" hx-target="#auth" hx-swap="outerHTML">
    <input name="email" type="email" placeholder="Email" value="{{.Email}}" required />
    <input name="password" type="password" placeholder="Password" required />
    <button type="submit">Login</button>
  </form>
  <p>No account? <a href="#" hx-get="/auth/register-form" hx-target="#auth" hx-swap="outerHTML">Create one</a></p>
</div>`))

var registerTmpl = template.Must(template.New("register").Parse(`<div id="auth">
  <h2>Create Account</h2>
  {{if .Error}}<p class="error">{{.Error}}</p>{{end}}
  <form hx-post="/auth/register" hx-target="#auth" hx-swap="outerHTML">
    <input name="email" type="email" placeholder="Email" value="{{.Email}}" required />
    <input name="password" type="password" placeholder="Password" required />
    <input name="passwordConfirm" type="password" placeholder="Confirm Password" required />
    <button type="submit">Create Account</button>
  </form>
  <p>Already have an account? <a href="#" hx-get="/auth/login-form" hx-target="#auth" hx-swap="outerHTML">Login</a></p>
</div>`))

var dashboardTmpl = template.Must(template.New("dashboard").Parse(`<div id="auth">
  <p>Welcome, <strong>{{.Email}}</strong>!</p>
  <button hx-post="/auth/logout" hx-target="#auth" hx-swap="outerHTML">Logout</button>
</div>`))

type pbAuthResponse struct {
	Token  string `json:"token"`
	Record struct {
		Email string `json:"email"`
	} `json:"record"`
}

type pbErrorResponse struct {
	Message string `json:"message"`
	Data    map[string]struct {
		Message string `json:"message"`
	} `json:"data"`
}

func pbPost(path string, payload any, token string) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", pbURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	return http.DefaultClient.Do(req)
}

func setAuthCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "pb_token",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   7 * 24 * 3600,
	})
}

func clearAuthCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:   "pb_token",
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
}

func pbErrMsg(resp *http.Response, fallback string) string {
	var pbErr pbErrorResponse
	json.NewDecoder(resp.Body).Decode(&pbErr)
	if pbErr.Message != "" {
		for _, v := range pbErr.Data {
			if v.Message != "" {
				return v.Message
			}
		}
		return pbErr.Message
	}
	return fallback
}

func main() {
	mux := http.NewServeMux()

	mux.Handle("/", http.FileServer(http.Dir("www")))

	mux.HandleFunc("GET /auth/status", func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("pb_token")
		if err != nil || cookie.Value == "" {
			loginTmpl.Execute(w, tmplData{})
			return
		}
		resp, err := pbPost("/api/collections/users/auth-refresh", nil, cookie.Value)
		if err != nil || resp.StatusCode != 200 {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			clearAuthCookie(w)
			loginTmpl.Execute(w, tmplData{})
			return
		}
		var auth pbAuthResponse
		json.NewDecoder(resp.Body).Decode(&auth)
		resp.Body.Close()
		setAuthCookie(w, auth.Token) // refresh the cookie with the new token
		dashboardTmpl.Execute(w, tmplData{Email: auth.Record.Email})
	})

	mux.HandleFunc("GET /auth/login-form", func(w http.ResponseWriter, r *http.Request) {
		loginTmpl.Execute(w, tmplData{})
	})

	mux.HandleFunc("GET /auth/register-form", func(w http.ResponseWriter, r *http.Request) {
		registerTmpl.Execute(w, tmplData{})
	})

	mux.HandleFunc("POST /auth/login", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		email := r.FormValue("email")
		password := r.FormValue("password")

		resp, err := pbPost("/api/collections/users/auth-with-password", map[string]string{
			"identity": email,
			"password": password,
		}, "")
		if err != nil {
			loginTmpl.Execute(w, tmplData{Error: "Could not reach auth service.", Email: email})
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			msg := pbErrMsg(resp, "Invalid email or password.")
			loginTmpl.Execute(w, tmplData{Error: msg, Email: email})
			return
		}

		var auth pbAuthResponse
		json.NewDecoder(resp.Body).Decode(&auth)
		setAuthCookie(w, auth.Token)
		dashboardTmpl.Execute(w, tmplData{Email: auth.Record.Email})
	})

	mux.HandleFunc("POST /auth/register", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		email := r.FormValue("email")
		password := r.FormValue("password")
		confirm := r.FormValue("passwordConfirm")

		resp, err := pbPost("/api/collections/users/records", map[string]string{
			"email":           email,
			"password":        password,
			"passwordConfirm": confirm,
		}, "")
		if err != nil {
			registerTmpl.Execute(w, tmplData{Error: "Could not reach auth service.", Email: email})
			return
		}

		if resp.StatusCode != 200 {
			msg := pbErrMsg(resp, "Registration failed.")
			resp.Body.Close()
			registerTmpl.Execute(w, tmplData{Error: msg, Email: email})
			return
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		// Auto-login after successful registration
		loginResp, err := pbPost("/api/collections/users/auth-with-password", map[string]string{
			"identity": email,
			"password": password,
		}, "")
		if err != nil || loginResp.StatusCode != 200 {
			if loginResp != nil {
				io.Copy(io.Discard, loginResp.Body)
				loginResp.Body.Close()
			}
			// Registration succeeded but login failed (e.g. email verification required)
			loginTmpl.Execute(w, tmplData{
				Success: "Account created! Please log in.",
				Email:   email,
			})
			return
		}
		defer loginResp.Body.Close()

		var auth pbAuthResponse
		json.NewDecoder(loginResp.Body).Decode(&auth)
		setAuthCookie(w, auth.Token)
		dashboardTmpl.Execute(w, tmplData{Email: auth.Record.Email})
	})

	mux.HandleFunc("POST /auth/logout", func(w http.ResponseWriter, r *http.Request) {
		clearAuthCookie(w)
		loginTmpl.Execute(w, tmplData{})
	})

	fmt.Println("Listening on http://localhost:8080")
	http.ListenAndServe(":8080", mux)
}
