package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// PocketBase URL. In Docker set PB_URL=http://pocketbase:8090
var pbURL = func() string {
	if u := os.Getenv("PB_URL"); u != "" {
		return u
	}
	return "http://localhost:8090"
}()

// Path to the shared data volume.
// On host (local dev): "data"
// In the baristex container: set MOCHATEX_DATA_DIR=/data
var mochatexDataDir = func() string {
	if d := os.Getenv("MOCHATEX_DATA_DIR"); d != "" {
		return d
	}
	return "data"
}()

// --- Data types ---

type authData struct {
	Error   string
	Success string
	Email   string
}

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

type pbDocument struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Template string          `json:"template"`
	Details  json.RawMessage `json:"details"`
	Assets   []string        `json:"assets"`
	Owner    string          `json:"owner"`
}

type pbListResp struct {
	Items []pbDocument `json:"items"`
}

type docViewModel struct {
	ID       string
	Name     string
	Template string
	Details  string
	Assets   []string
	PDFReady bool
}

type docsData struct {
	Docs  []docViewModel
	Error string
}

type docEditData struct {
	Doc   docViewModel
	Error string
}

type docNewData struct {
	Error string
}

type generateData struct {
	ID       string
	PDFReady bool
	Error    string
}

// --- Templates ---

var tmpl = template.Must(template.New("root").Funcs(template.FuncMap{
	"urlenc":     url.QueryEscape,
	"stripasset": stripPBSuffix,
}).Parse(`
{{define "login"}}<div id="auth">
  <h2>Login</h2>
  {{if .Error}}<p class="error">{{.Error}}</p>{{end}}
  {{if .Success}}<p class="success">{{.Success}}</p>{{end}}
  <form hx-post="/auth/login" hx-target="#auth" hx-swap="outerHTML">
    <input name="email" type="email" placeholder="Email" value="{{.Email}}" required />
    <input name="password" type="password" placeholder="Password" required />
    <button type="submit">Login</button>
  </form>
  <p>No account? <a href="#" hx-get="/auth/register-form" hx-target="#auth" hx-swap="outerHTML">Create one</a></p>
</div>{{end}}

{{define "register"}}<div id="auth">
  <h2>Create Account</h2>
  {{if .Error}}<p class="error">{{.Error}}</p>{{end}}
  <form hx-post="/auth/register" hx-target="#auth" hx-swap="outerHTML">
    <input name="email" type="email" placeholder="Email" value="{{.Email}}" required />
    <input name="password" type="password" placeholder="Password" required />
    <input name="passwordConfirm" type="password" placeholder="Confirm Password" required />
    <button type="submit">Create Account</button>
  </form>
  <p>Already have an account? <a href="#" hx-get="/auth/login-form" hx-target="#auth" hx-swap="outerHTML">Login</a></p>
</div>{{end}}

{{define "dashboard"}}<div id="auth">
  <div class="auth-header">
    <span>Welcome, <strong>{{.Email}}</strong></span>
    <button hx-post="/auth/logout" hx-target="#auth" hx-swap="outerHTML">Logout</button>
  </div>
  <div id="documents" hx-get="/documents" hx-trigger="load" hx-swap="innerHTML">
    <p class="muted">Loading documents&hellip;</p>
  </div>
</div>{{end}}

{{define "docs"}}
<div class="docs-header">
  <h2>Documents</h2>
  <button hx-get="/documents/new" hx-target="#documents" hx-swap="innerHTML">+ New</button>
</div>
{{if .Error}}<p class="error">{{.Error}}</p>{{end}}
{{if .Docs}}
<ul id="doc-list">{{range .Docs}}{{template "docCard" .}}{{end}}</ul>
{{else}}<p class="muted">No documents yet.</p>
{{end}}{{end}}

{{define "docCard"}}<li id="doc-{{.ID}}" class="doc-card">
  <div class="doc-info">
    <strong>{{.Name}}</strong>
    <code class="doc-template">{{.Template}}</code>
  </div>
  <pre class="doc-details">{{.Details}}</pre>
  <div class="doc-assets">
    {{if .Assets}}
    <div class="asset-list">
      {{$id := .ID}}
      {{range .Assets}}<span class="asset-tag">
        <code>{{stripasset .}}</code>
        <button class="btn-xs btn-danger"
                hx-delete="/documents/{{$id}}/assets?file={{urlenc .}}"
                hx-target="#doc-{{$id}}" hx-swap="outerHTML"
                hx-confirm="Remove {{stripasset .}}?">&#x2715;</button>
      </span>{{end}}
    </div>
    {{end}}
    <form hx-post="/documents/{{.ID}}/assets" hx-target="#doc-{{.ID}}" hx-swap="outerHTML" hx-encoding="multipart/form-data">
      <div class="doc-actions">
        <input name="assets" type="file" multiple />
        <button type="submit">Add Assets</button>
      </div>
    </form>
  </div>
  <div class="doc-actions">
    <button hx-get="/documents/{{.ID}}/edit" hx-target="#doc-{{.ID}}" hx-swap="outerHTML">Edit Details</button>
    <button hx-post="/documents/{{.ID}}/generate" hx-target="#gen-{{.ID}}" hx-swap="innerHTML" hx-disabled-elt="this">Generate PDF</button>
    <button class="btn-danger" hx-delete="/documents/{{.ID}}" hx-target="#doc-{{.ID}}" hx-swap="outerHTML" hx-confirm="Delete &#39;{{.Name}}&#39;?">Delete</button>
  </div>
  <div id="gen-{{.ID}}" class="generate-result">
    {{if .PDFReady}}<a class="btn-download" href="/documents/{{.ID}}/pdf">&#11015; Download PDF</a>{{end}}
  </div>
</li>{{end}}

{{define "docEditCard"}}<li id="doc-{{.Doc.ID}}" class="doc-card editing">
  <div class="doc-info">
    <strong>{{.Doc.Name}}</strong>
    <code class="doc-template">{{.Doc.Template}}</code>
  </div>
  {{if .Error}}<p class="error">{{.Error}}</p>{{end}}
  <form hx-patch="/documents/{{.Doc.ID}}" hx-target="#doc-{{.Doc.ID}}" hx-swap="outerHTML">
    <textarea name="details" rows="10" spellcheck="false">{{.Doc.Details}}</textarea>
    <div class="doc-actions">
      <button type="submit">Save</button>
      <button type="button" hx-get="/documents/{{.Doc.ID}}/card" hx-target="#doc-{{.Doc.ID}}" hx-swap="outerHTML">Cancel</button>
    </div>
  </form>
</li>{{end}}

{{define "docNewForm"}}<div class="doc-card">
  <h3>New Document</h3>
  {{if .Error}}<p class="error">{{.Error}}</p>{{end}}
  <form hx-post="/documents" hx-target="#documents" hx-swap="innerHTML" hx-encoding="multipart/form-data">
    <input name="name" type="text" placeholder="Document name" required />
    <label>Template (.tex) <input name="template" type="file" accept=".tex" required /></label>
    <label>Details (.json) <input name="details_file" type="file" accept=".json" /></label>
    <div class="doc-actions">
      <button type="submit">Upload</button>
      <button type="button" hx-get="/documents" hx-target="#documents" hx-swap="innerHTML">Cancel</button>
    </div>
  </form>
</div>{{end}}

{{define "generateResult"}}
{{if .Error}}<p class="error">{{.Error}}</p>
{{else}}<a class="btn-download" href="/documents/{{.ID}}/pdf">&#11015; Download PDF</a>
{{end}}
{{end}}
`))

// --- Helpers ---

func authToken(r *http.Request) string {
	c, err := r.Cookie("pb_token")
	if err != nil {
		return ""
	}
	return c.Value
}

func userIDFromToken(token string) string {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		ID string `json:"id"`
	}
	json.Unmarshal(payload, &claims)
	return claims.ID
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

func pbJSON(method, path string, body any, token string) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, pbURL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	return http.DefaultClient.Do(req)
}

func pbErrMsg(resp *http.Response, fallback string) string {
	var e pbErrorResponse
	json.NewDecoder(resp.Body).Decode(&e)
	for _, v := range e.Data {
		if v.Message != "" {
			return v.Message
		}
	}
	if e.Message != "" {
		return e.Message
	}
	return fallback
}

func toViewModel(doc pbDocument) docViewModel {
	details := "{}"
	if len(doc.Details) > 0 && strings.TrimSpace(string(doc.Details)) != "null" {
		if pretty, err := json.MarshalIndent(json.RawMessage(doc.Details), "", "  "); err == nil {
			details = string(pretty)
		}
	}
	return docViewModel{
		ID:       doc.ID,
		Name:     doc.Name,
		Template: doc.Template,
		Details:  details,
		Assets:   doc.Assets,
		PDFReady: pdfExists(doc.ID),
	}
}

func getDocList(token string) ([]docViewModel, error) {
	resp, err := pbJSON("GET", "/api/collections/documents/records?sort=-updated", nil, token)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var list pbListResp
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	vms := make([]docViewModel, len(list.Items))
	for i, d := range list.Items {
		vms[i] = toViewModel(d)
	}
	return vms, nil
}

func getDoc(id, token string) (pbDocument, error) {
	resp, err := pbJSON("GET", "/api/collections/documents/records/"+id, nil, token)
	if err != nil {
		return pbDocument{}, err
	}
	defer resp.Body.Close()
	var doc pbDocument
	return doc, json.NewDecoder(resp.Body).Decode(&doc)
}

func pdfExists(id string) bool {
	pdfs, _ := filepath.Glob(filepath.Join(mochatexDataDir, id, "*.pdf"))
	return len(pdfs) > 0
}

// stripPBSuffix removes the random hash PocketBase appends to stored filenames.
// e.g. "logo_vxy38dkm2q.png" → "logo.png"
func stripPBSuffix(pbName string) string {
	ext := filepath.Ext(pbName)
	stem := strings.TrimSuffix(pbName, ext)
	i := strings.LastIndex(stem, "_")
	if i <= 0 {
		return pbName
	}
	suffix := stem[i+1:]
	if len(suffix) < 8 {
		return pbName
	}
	for _, c := range suffix {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
			return pbName
		}
	}
	return stem[:i] + ext
}

func sanitizeFilename(name string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, name)
}

func execTmpl(w http.ResponseWriter, name string, data any) {
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func main() {
	mux := http.NewServeMux()

	mux.Handle("/", http.FileServer(http.Dir("www")))

	// --- Auth ---

	mux.HandleFunc("GET /auth/status", func(w http.ResponseWriter, r *http.Request) {
		token := authToken(r)
		if token == "" {
			execTmpl(w, "login", authData{})
			return
		}
		resp, err := pbJSON("POST", "/api/collections/users/auth-refresh", nil, token)
		if err != nil || resp.StatusCode != 200 {
			if resp != nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
			clearAuthCookie(w)
			execTmpl(w, "login", authData{})
			return
		}
		var auth pbAuthResponse
		json.NewDecoder(resp.Body).Decode(&auth)
		resp.Body.Close()
		setAuthCookie(w, auth.Token)
		execTmpl(w, "dashboard", authData{Email: auth.Record.Email})
	})

	mux.HandleFunc("GET /auth/login-form", func(w http.ResponseWriter, r *http.Request) {
		execTmpl(w, "login", authData{})
	})

	mux.HandleFunc("GET /auth/register-form", func(w http.ResponseWriter, r *http.Request) {
		execTmpl(w, "register", authData{})
	})

	mux.HandleFunc("POST /auth/login", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		email := r.FormValue("email")
		password := r.FormValue("password")

		resp, err := pbJSON("POST", "/api/collections/users/auth-with-password", map[string]string{
			"identity": email,
			"password": password,
		}, "")
		if err != nil {
			execTmpl(w, "login", authData{Error: "Could not reach auth service.", Email: email})
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			execTmpl(w, "login", authData{Error: pbErrMsg(resp, "Invalid email or password."), Email: email})
			return
		}

		var auth pbAuthResponse
		json.NewDecoder(resp.Body).Decode(&auth)
		setAuthCookie(w, auth.Token)
		execTmpl(w, "dashboard", authData{Email: auth.Record.Email})
	})

	mux.HandleFunc("POST /auth/register", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		email := r.FormValue("email")
		password := r.FormValue("password")
		confirm := r.FormValue("passwordConfirm")

		resp, err := pbJSON("POST", "/api/collections/users/records", map[string]string{
			"email":           email,
			"password":        password,
			"passwordConfirm": confirm,
		}, "")
		if err != nil {
			execTmpl(w, "register", authData{Error: "Could not reach auth service.", Email: email})
			return
		}
		if resp.StatusCode != 200 {
			msg := pbErrMsg(resp, "Registration failed.")
			resp.Body.Close()
			execTmpl(w, "register", authData{Error: msg, Email: email})
			return
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		loginResp, err := pbJSON("POST", "/api/collections/users/auth-with-password", map[string]string{
			"identity": email,
			"password": password,
		}, "")
		if err != nil || loginResp.StatusCode != 200 {
			if loginResp != nil {
				io.Copy(io.Discard, loginResp.Body)
				loginResp.Body.Close()
			}
			execTmpl(w, "login", authData{Success: "Account created! Please log in.", Email: email})
			return
		}
		defer loginResp.Body.Close()

		var auth pbAuthResponse
		json.NewDecoder(loginResp.Body).Decode(&auth)
		setAuthCookie(w, auth.Token)
		execTmpl(w, "dashboard", authData{Email: auth.Record.Email})
	})

	mux.HandleFunc("POST /auth/logout", func(w http.ResponseWriter, r *http.Request) {
		clearAuthCookie(w)
		execTmpl(w, "login", authData{})
	})

	// --- Documents ---

	mux.HandleFunc("GET /documents", func(w http.ResponseWriter, r *http.Request) {
		token := authToken(r)
		if token == "" {
			execTmpl(w, "docs", docsData{Error: "Not authenticated."})
			return
		}
		docs, err := getDocList(token)
		if err != nil {
			execTmpl(w, "docs", docsData{Error: "Failed to load documents."})
			return
		}
		execTmpl(w, "docs", docsData{Docs: docs})
	})

	mux.HandleFunc("GET /documents/new", func(w http.ResponseWriter, r *http.Request) {
		execTmpl(w, "docNewForm", docNewData{})
	})

	mux.HandleFunc("POST /documents", func(w http.ResponseWriter, r *http.Request) {
		token := authToken(r)
		if token == "" {
			execTmpl(w, "docNewForm", docNewData{Error: "Not authenticated."})
			return
		}
		ownerID := userIDFromToken(token)
		if ownerID == "" {
			execTmpl(w, "docNewForm", docNewData{Error: "Could not determine user identity."})
			return
		}

		if err := r.ParseMultipartForm(10 << 20); err != nil {
			execTmpl(w, "docNewForm", docNewData{Error: "Failed to parse form."})
			return
		}

		name := r.FormValue("name")
		templateFile, templateHeader, err := r.FormFile("template")
		if err != nil {
			execTmpl(w, "docNewForm", docNewData{Error: "Template file is required."})
			return
		}
		defer templateFile.Close()

		// Read optional details JSON file
		detailsJSON := "{}"
		if detailsFile, _, err := r.FormFile("details_file"); err == nil {
			defer detailsFile.Close()
			if raw, err := io.ReadAll(detailsFile); err == nil {
				var check json.RawMessage
				if json.Unmarshal(raw, &check) == nil {
					detailsJSON = string(raw)
				}
			}
		}

		// Build multipart request for PocketBase
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		mw.WriteField("name", name)
		mw.WriteField("owner", ownerID)
		mw.WriteField("details", detailsJSON)
		fw, err := mw.CreateFormFile("template", templateHeader.Filename)
		if err != nil {
			execTmpl(w, "docNewForm", docNewData{Error: "Failed to prepare upload."})
			return
		}
		if _, err := io.Copy(fw, templateFile); err != nil {
			execTmpl(w, "docNewForm", docNewData{Error: "Failed to read template file."})
			return
		}
		mw.Close()

		req, _ := http.NewRequest("POST", pbURL+"/api/collections/documents/records", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Authorization", token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			execTmpl(w, "docNewForm", docNewData{Error: "Could not reach storage service."})
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			execTmpl(w, "docNewForm", docNewData{Error: pbErrMsg(resp, "Failed to create document.")})
			return
		}
		io.Copy(io.Discard, resp.Body)

		docs, _ := getDocList(token)
		execTmpl(w, "docs", docsData{Docs: docs})
	})

	mux.HandleFunc("GET /documents/{id}/card", func(w http.ResponseWriter, r *http.Request) {
		token := authToken(r)
		doc, err := getDoc(r.PathValue("id"), token)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		execTmpl(w, "docCard", toViewModel(doc))
	})

	mux.HandleFunc("GET /documents/{id}/edit", func(w http.ResponseWriter, r *http.Request) {
		token := authToken(r)
		doc, err := getDoc(r.PathValue("id"), token)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		execTmpl(w, "docEditCard", docEditData{Doc: toViewModel(doc)})
	})

	mux.HandleFunc("PATCH /documents/{id}", func(w http.ResponseWriter, r *http.Request) {
		token := authToken(r)
		id := r.PathValue("id")
		r.ParseForm()
		detailsStr := r.FormValue("details")

		var detailsJSON json.RawMessage
		if err := json.Unmarshal([]byte(detailsStr), &detailsJSON); err != nil {
			doc, _ := getDoc(id, token)
			vm := toViewModel(doc)
			vm.Details = detailsStr
			execTmpl(w, "docEditCard", docEditData{Doc: vm, Error: "Invalid JSON: " + err.Error()})
			return
		}

		resp, err := pbJSON("PATCH", "/api/collections/documents/records/"+id, map[string]any{
			"details": detailsJSON,
		}, token)
		if err != nil {
			doc, _ := getDoc(id, token)
			execTmpl(w, "docEditCard", docEditData{Doc: toViewModel(doc), Error: "Save failed."})
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			doc, _ := getDoc(id, token)
			vm := toViewModel(doc)
			vm.Details = detailsStr
			execTmpl(w, "docEditCard", docEditData{Doc: vm, Error: pbErrMsg(resp, "Save failed.")})
			return
		}
		io.Copy(io.Discard, resp.Body)

		updated, _ := getDoc(id, token)
		execTmpl(w, "docCard", toViewModel(updated))
	})

	mux.HandleFunc("DELETE /documents/{id}", func(w http.ResponseWriter, r *http.Request) {
		token := authToken(r)
		id := r.PathValue("id")

		resp, err := pbJSON("DELETE", "/api/collections/documents/records/"+id, nil, token)
		if err != nil || resp.StatusCode != 204 {
			if resp != nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
			doc, _ := getDoc(id, token)
			execTmpl(w, "docCard", toViewModel(doc))
			return
		}
		resp.Body.Close()
		// Empty response — HTMX removes the element via outerHTML swap
	})

	// --- Assets ---

	mux.HandleFunc("POST /documents/{id}/assets", func(w http.ResponseWriter, r *http.Request) {
		token := authToken(r)
		id := r.PathValue("id")

		if err := r.ParseMultipartForm(50 << 20); err != nil {
			doc, _ := getDoc(id, token)
			execTmpl(w, "docCard", toViewModel(doc))
			return
		}

		files := r.MultipartForm.File["assets"]
		if len(files) == 0 {
			doc, _ := getDoc(id, token)
			execTmpl(w, "docCard", toViewModel(doc))
			return
		}

		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		for _, fh := range files {
			fw, err := mw.CreateFormFile("assets", fh.Filename)
			if err != nil {
				continue
			}
			f, err := fh.Open()
			if err != nil {
				continue
			}
			io.Copy(fw, f)
			f.Close()
		}
		mw.Close()

		req, _ := http.NewRequest("PATCH", pbURL+"/api/collections/documents/records/"+id, &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Authorization", token)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}

		doc, _ := getDoc(id, token)
		execTmpl(w, "docCard", toViewModel(doc))
	})

	mux.HandleFunc("DELETE /documents/{id}/assets", func(w http.ResponseWriter, r *http.Request) {
		token := authToken(r)
		id := r.PathValue("id")
		filename := r.URL.Query().Get("file")

		if filename != "" {
			var buf bytes.Buffer
			mw := multipart.NewWriter(&buf)
			mw.WriteField("assets-", filename)
			mw.Close()

			req, _ := http.NewRequest("PATCH", pbURL+"/api/collections/documents/records/"+id, &buf)
			req.Header.Set("Content-Type", mw.FormDataContentType())
			req.Header.Set("Authorization", token)
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}

		doc, _ := getDoc(id, token)
		execTmpl(w, "docCard", toViewModel(doc))
	})

	// --- PDF generation ---

	mux.HandleFunc("POST /documents/{id}/generate", func(w http.ResponseWriter, r *http.Request) {
		token := authToken(r)
		id := r.PathValue("id")

		doc, err := getDoc(id, token)
		if err != nil {
			execTmpl(w, "generateResult", generateData{ID: id, Error: "Document not found."})
			return
		}

		// Prepare work directory inside the shared volume
		workDir := filepath.Join(mochatexDataDir, id)
		if err := os.MkdirAll(workDir, 0755); err != nil {
			execTmpl(w, "generateResult", generateData{ID: id, Error: "Failed to create work directory: " + err.Error()})
			return
		}

		// Download template file from PocketBase
		templateURL := fmt.Sprintf("/api/files/documents/%s/%s", id, doc.Template)
		resp, err := pbJSON("GET", templateURL, nil, token)
		if err != nil || resp.StatusCode != 200 {
			if resp != nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
			execTmpl(w, "generateResult", generateData{ID: id, Error: "Failed to download template."})
			return
		}
		templatePath := filepath.Join(workDir, "template.tex")
		f, _ := os.Create(templatePath)
		io.Copy(f, resp.Body)
		f.Close()
		resp.Body.Close()

		// Write details JSON to file
		details := doc.Details
		if len(details) == 0 || strings.TrimSpace(string(details)) == "null" {
			details = json.RawMessage("{}")
		}
		if err := os.WriteFile(filepath.Join(workDir, "details.json"), details, 0644); err != nil {
			execTmpl(w, "generateResult", generateData{ID: id, Error: "Failed to write details."})
			return
		}

		// Download asset files so they are available next to the template
		for _, asset := range doc.Assets {
			assetURL := fmt.Sprintf("/api/files/documents/%s/%s", id, asset)
			resp, err := pbJSON("GET", assetURL, nil, token)
			if err != nil || resp.StatusCode != 200 {
				if resp != nil {
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
				execTmpl(w, "generateResult", generateData{ID: id, Error: "Failed to download asset: " + asset})
				return
			}
			f, _ := os.Create(filepath.Join(workDir, stripPBSuffix(asset)))
			io.Copy(f, resp.Body)
			f.Close()
			resp.Body.Close()
		}

		// Run mochatex inside the persistent container (volume already mounted)
		containerDir := "/data/" + id
		cmd := exec.Command("docker", "exec", "mochatex",
			"mochatex",
			"-t", containerDir+"/template.tex",
			"-d", containerDir+"/details.json",
			containerDir,
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if msg == "" {
				msg = err.Error()
			}
			execTmpl(w, "generateResult", generateData{ID: id, Error: "Generation failed: " + msg})
			return
		}

		// Check a PDF was actually produced
		pdfs, _ := filepath.Glob(filepath.Join(workDir, "*.pdf"))
		if len(pdfs) == 0 {
			execTmpl(w, "generateResult", generateData{ID: id, Error: "No PDF was produced."})
			return
		}

		// Rename to <document_name>.pdf
		pdfName := sanitizeFilename(doc.Name) + ".pdf"
		os.Rename(pdfs[0], filepath.Join(workDir, pdfName))

		execTmpl(w, "generateResult", generateData{ID: id, PDFReady: true})
	})

	mux.HandleFunc("GET /documents/{id}/pdf", func(w http.ResponseWriter, r *http.Request) {
		token := authToken(r)
		id := r.PathValue("id")

		// Verify the user owns this document
		if _, err := getDoc(id, token); err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		pdfs, _ := filepath.Glob(filepath.Join(mochatexDataDir, id, "*.pdf"))
		if len(pdfs) == 0 {
			http.Error(w, "PDF not found — generate it first", http.StatusNotFound)
			return
		}

		pdfPath := pdfs[0]
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(pdfPath)))
		http.ServeFile(w, r, pdfPath)
	})

	fmt.Println("Listening on http://localhost:8080")
	http.ListenAndServe(":8080", mux)
}
