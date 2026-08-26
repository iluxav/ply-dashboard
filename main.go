// ply-dashboard — the opt-in web UI that is just a ply app.
//
// Reads ply's on-disk state through explicitly granted bind mounts, renders
// dense server-side HTML with htmx polling, and mutates nothing (v1).
// Single static binary, assets embedded, no database.
package main

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/iluxav/ply-dashboard/internal/auth"
	"github.com/iluxav/ply-dashboard/internal/plystate"
	"github.com/iluxav/ply-dashboard/internal/registry"

	// The debian-slim base ships no CA bundle; these are the Mozilla roots
	// compiled into the binary, used only when the system pool is empty —
	// TLS (the registry catalog) works in a minimal container.
	_ "golang.org/x/crypto/x509roots/fallback"
)

//go:embed web
var webFS embed.FS

var version = "dev" // set by -ldflags at release

type server struct {
	paths    plystate.Paths
	sampler  *plystate.Sampler
	auth     *auth.Auth
	registry *registry.Client
	pages    map[string]*template.Template
}

func main() {
	port := envOr("PORT", "7070")
	dataDir := envOr("DATA_DIR", defaultDataDir())

	a, err := auth.Load(dataDir)
	if err != nil {
		log.Fatalf("auth: %v", err)
	}
	paths := plystate.Resolve()
	sampler := plystate.NewSampler(paths, 3*time.Second)
	go sampler.Run()

	s := &server{paths: paths, sampler: sampler, auth: a, registry: registry.NewClient()}
	s.parseTemplates()

	mux := http.NewServeMux()
	assets, _ := fs.Sub(webFS, "web/assets")
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServerFS(assets)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	mux.HandleFunc("GET /setup", s.setupPage)
	mux.HandleFunc("POST /setup", s.setupSubmit)
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.loginSubmit)
	mux.HandleFunc("POST /logout", s.logout)

	mux.HandleFunc("GET /{$}", s.guard(s.overview))
	mux.HandleFunc("GET /app/{name}", s.guard(s.appPage))
	mux.HandleFunc("GET /partials/apps", s.guard(s.appsPartial))
	mux.HandleFunc("GET /partials/app/{name}", s.guard(s.appPartial))
	mux.HandleFunc("GET /partials/logs/{name}", s.guard(s.logsPartial))
	mux.HandleFunc("POST /app/{name}/scale", s.guard(s.scaleAction))
	mux.HandleFunc("POST /app/{name}/restart", s.guard(s.restartAction))
	mux.HandleFunc("GET /deploy", s.guard(s.deployPage))
	mux.HandleFunc("POST /deploy", s.guard(s.deployCreate))
	mux.HandleFunc("POST /deploy/{name}/delete", s.guard(s.deployDelete))
	mux.HandleFunc("GET /partials/deployments", s.guard(s.deploymentsPartial))

	log.Printf("ply-dashboard %s — listening on :%s (state: %s)", version, port, paths.State)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

// --- template plumbing -------------------------------------------------------

// Each page parses base + its own file + the partials it includes; partials
// also render standalone for htmx swaps.
func (s *server) parseTemplates() {
	funcs := template.FuncMap{
		"statsOf":        s.sampler.Stats,
		"contract":       registry.Contract,
		"defaultPublish": registry.DefaultPublish,
	}
	page := func(files ...string) *template.Template {
		paths := append([]string{"web/templates/base.html"}, files...)
		return template.Must(template.New("base.html").Funcs(funcs).ParseFS(webFS, paths...))
	}
	s.pages = map[string]*template.Template{
		"index":  page("web/templates/index.html", "web/templates/apps_table.html"),
		"app":    page("web/templates/app.html", "web/templates/instances.html", "web/templates/logs.html"),
		"deploy": page("web/templates/deploy.html", "web/templates/deployments.html"),
		"login":  page("web/templates/login.html"),
		"setup":  page("web/templates/setup.html"),
		// standalone partials for htmx polling
		"apps_table":  template.Must(template.New("p").Funcs(funcs).ParseFS(webFS, "web/templates/apps_table.html")),
		"instances":   template.Must(template.New("p").Funcs(funcs).ParseFS(webFS, "web/templates/instances.html")),
		"logs":        template.Must(template.New("p").Funcs(funcs).ParseFS(webFS, "web/templates/logs.html")),
		"deployments": template.Must(template.New("p").Funcs(funcs).ParseFS(webFS, "web/templates/deployments.html")),
	}
}

type pageData struct {
	Authed  bool
	Version string
	Error   string

	Apps       []plystate.App
	AppName    string
	App        plystate.App
	Instances  []plystate.Instance
	Commands   []command
	LogLines   []string
	Writable   bool
	ScaleUp    int
	ScaleDown  int
	LastResult *plystate.CommandResult

	DeployAvailable bool
	RegistryApps    []registry.App
	RegistryErr     string
	DeployErr       string
	Deployments     []plystate.Deployment
}

func (s *server) render(w http.ResponseWriter, page, name string, data pageData) {
	data.Version = version
	if err := s.pages[page].ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render %s: %v", page, err)
	}
}

// --- auth flow ---------------------------------------------------------------

func (s *server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.auth.NeedsSetup() {
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}
		if !s.auth.Valid(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

func (s *server) setupPage(w http.ResponseWriter, r *http.Request) {
	if !s.auth.NeedsSetup() {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.render(w, "setup", "base.html", pageData{})
}

func (s *server) setupSubmit(w http.ResponseWriter, r *http.Request) {
	err := s.auth.Setup(r.FormValue("token"), r.FormValue("user"), r.FormValue("password"))
	if err != nil {
		s.render(w, "setup", "base.html", pageData{Error: err.Error()})
		return
	}
	cookie, err := s.auth.Login(r.RemoteAddr, r.FormValue("user"), r.FormValue("password"))
	if err == nil {
		s.auth.SetCookie(w, cookie, secure(r))
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *server) loginPage(w http.ResponseWriter, r *http.Request) {
	if s.auth.NeedsSetup() {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	s.render(w, "login", "base.html", pageData{})
}

func (s *server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	cookie, err := s.auth.Login(r.RemoteAddr, r.FormValue("user"), r.FormValue("password"))
	if err != nil {
		s.render(w, "login", "base.html", pageData{Error: err.Error()})
		return
	}
	s.auth.SetCookie(w, cookie, secure(r))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	s.auth.ClearCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func secure(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// --- views -------------------------------------------------------------------

func (s *server) overview(w http.ResponseWriter, r *http.Request) {
	apps := s.apps()
	s.render(w, "index", "base.html", pageData{Authed: true, Apps: apps})
}

func (s *server) appsPartial(w http.ResponseWriter, r *http.Request) {
	s.render(w, "apps_table", "apps_table", pageData{Apps: s.apps()})
}

func (s *server) appPage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	app, ok := s.app(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	data := s.liveData(name, app)
	data.Authed = true
	data.Commands = commandsFor(app)
	data.LogLines = s.logLines(app)
	s.render(w, "app", "base.html", data)
}

func (s *server) appPartial(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	app, ok := s.app(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.render(w, "instances", "instances", s.liveData(name, app))
}

// liveData: everything inside the polled #live section, buttons included.
func (s *server) liveData(name string, app plystate.App) pageData {
	n := len(app.Instances)
	return pageData{
		AppName:    name,
		App:        app,
		Instances:  app.Instances,
		Writable:   plystate.ControlWritable(s.paths, name),
		ScaleUp:    n + 1,
		ScaleDown:  max(n-1, 1),
		LastResult: plystate.LastResult(s.paths, name),
	}
}

func (s *server) scaleAction(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	app, ok := s.app(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	to := r.URL.Query().Get("to")
	if n, err := strconv.Atoi(to); err == nil && n >= 1 && n <= 100 {
		if err := plystate.SubmitControl(s.paths, name, "scale", to); err != nil {
			log.Printf("scale %s: %v", name, err)
		}
	}
	s.render(w, "instances", "instances", s.liveData(name, app))
}

func (s *server) restartAction(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	app, ok := s.app(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := plystate.SubmitControl(s.paths, name, "restart", ""); err != nil {
		log.Printf("restart %s: %v", name, err)
	}
	s.render(w, "instances", "instances", s.liveData(name, app))
}

func (s *server) logsPartial(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	app, ok := s.app(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.render(w, "logs", "logs", pageData{LogLines: s.logLines(app)})
}

func (s *server) deployPage(w http.ResponseWriter, r *http.Request) {
	s.renderDeploy(w, r.URL.Query().Get("err"))
}

func (s *server) renderDeploy(w http.ResponseWriter, deployErr string) {
	data := pageData{
		Authed:          true,
		DeployAvailable: plystate.DeploymentsAvailable(s.paths),
		Deployments:     plystate.Deployments(s.paths),
		DeployErr:       deployErr,
	}
	if data.DeployAvailable {
		apps, err := s.registry.Apps()
		data.RegistryApps = apps
		if err != nil {
			data.RegistryErr = err.Error()
		}
	}
	s.render(w, "deploy", "base.html", data)
}

func (s *server) deployCreate(w http.ResponseWriter, r *http.Request) {
	err := plystate.WriteDeployment(
		s.paths,
		strings.TrimSpace(r.FormValue("name")),
		strings.TrimSpace(r.FormValue("app")),
		strings.TrimSpace(r.FormValue("version")),
		strings.TrimSpace(r.FormValue("publish")),
		strings.TrimSpace(r.FormValue("domain")),
		r.FormValue("env"),
	)
	if err != nil {
		http.Redirect(w, r, "/deploy?err="+template.URLQueryEscaper(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/deploy", http.StatusSeeOther)
}

func (s *server) deployDelete(w http.ResponseWriter, r *http.Request) {
	if err := plystate.DeleteDeployment(s.paths, r.PathValue("name")); err != nil {
		log.Printf("delete deployment: %v", err)
	}
	http.Redirect(w, r, "/deploy", http.StatusSeeOther)
}

func (s *server) deploymentsPartial(w http.ResponseWriter, _ *http.Request) {
	s.render(w, "deployments", "deployments", pageData{
		Deployments: plystate.Deployments(s.paths),
	})
}

// logLines merges instance rings, prefixing when there are several.
func (s *server) logLines(app plystate.App) []string {
	prefix := len(app.Instances) > 1
	var out []string
	for _, inst := range app.Instances {
		for _, line := range plystate.LogTail(s.paths, inst.App, inst.N, 150) {
			if prefix {
				out = append(out, inst.Name()+" | "+line)
			} else {
				out = append(out, line)
			}
		}
	}
	if len(out) > 300 {
		out = out[len(out)-300:]
	}
	return out
}

func (s *server) apps() []plystate.App {
	instances, err := plystate.List(s.paths)
	if err != nil {
		log.Printf("state: %v", err)
	}
	return plystate.Apps(instances)
}

func (s *server) app(name string) (plystate.App, bool) {
	for _, a := range s.apps() {
		if a.Name == name {
			return a, true
		}
	}
	return plystate.App{}, false
}

// --- the command panel: v1's honest mutation story ---------------------------

type command struct {
	Cmd  string
	What string
}

func commandsFor(a plystate.App) []command {
	name := a.Name
	image := a.Oldest().Image
	return []command{
		{fmt.Sprintf("ply logs %s -f", name), "follow output in the terminal"},
		{fmt.Sprintf("ply deploy %s", image), "roll to a new build of this image path"},
		{fmt.Sprintf("ply exec %s sh", name), "shell into instance 1"},
		{fmt.Sprintf("journalctl -u ply-%s -f", name), "follow logs (systemd hosts)"},
		{fmt.Sprintf("ply stats %s", name), "live cpu/mem/net in the terminal"},
		{fmt.Sprintf("ply rm %s", name), "stop and remove (volumes kept)"},
	}
}

// --- misc --------------------------------------------------------------------

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func defaultDataDir() string {
	// the ply volume in production; a local dir in dev
	if info, err := os.Stat("/data"); err == nil && info.IsDir() {
		return "/data"
	}
	dir := ".ply-dashboard-data"
	_ = os.MkdirAll(dir, 0o700)
	return dir
}

var _ = strings.TrimSpace // reserved
