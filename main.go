// ply-dashboard — the opt-in web UI that is just a ply app.
//
// Reads ply's on-disk state through explicitly granted bind mounts, renders
// dense server-side HTML with htmx polling, and mutates nothing (v1).
// Single static binary, assets embedded, no database.
package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iluxav/ply-dashboard/internal/auth"
	"github.com/iluxav/ply-dashboard/internal/github"
	"github.com/iluxav/ply-dashboard/internal/plystate"
	"github.com/iluxav/ply-dashboard/internal/registry"
	"github.com/iluxav/ply-dashboard/internal/term"

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
	fresh    *freshness
	pages    map[string]*template.Template
}

// --- freshness: is a newer commit/release available? -------------------------

// One background sweep every 2 minutes; the 3s-polling partials only read
// the cache. Public checks are tokenless and rate-limit-free: smart-HTTP
// refs advertisement for repo lanes, the releases/latest redirect for
// github lanes. Private repos join when the spec carries a token_file;
// ssh-remote repos stay honestly unknowable and get no row.
type freshness struct {
	paths  plystate.Paths
	mu     sync.Mutex
	byName map[string]*remoteTip
	kicks  chan struct{}
}

// remoteTip is what the remote said, cached; the comparison against the
// deployed state happens at render time, so the moment a deploy lands the
// line flips without waiting for the next sweep.
type remoteTip struct {
	Kind   string // "version" | "commit"
	Latest string
}

type Freshness struct {
	Current         string
	Latest          string
	UpdateAvailable bool
	Building        bool // reconcile reported an in-flight build
}

func newFreshness(p plystate.Paths) *freshness {
	return &freshness{paths: p, byName: map[string]*remoteTip{}, kicks: make(chan struct{}, 1)}
}

func (f *freshness) run() {
	for {
		f.sweep()
		select {
		case <-time.After(2 * time.Minute):
		case <-f.kicks:
		}
	}
}

// Kick asks for an immediate sweep — called when deployments change, so a
// fresh row gets its line while the human is still looking at it.
func (f *freshness) Kick() {
	select {
	case f.kicks <- struct{}{}:
	default:
	}
}

func (f *freshness) sweep() {
	seen := map[string]bool{}
	for _, d := range plystate.Deployments(f.paths) {
		var tip *remoteTip
		switch {
		case d.Field("github") != "":
			latest, err := github.LatestRelease(d.Field("github"), f.secret(d))
			if err != nil {
				continue
			}
			tip = &remoteTip{Kind: "version", Latest: latest}
		case strings.HasPrefix(d.Field("repo"), "http"):
			sha, err := github.LsRemote(d.Field("repo"), d.Field("ref"), f.secret(d))
			if err != nil {
				continue
			}
			tip = &remoteTip{Kind: "commit", Latest: sha}
		default:
			continue // registry/image lanes and ssh remotes: nothing to probe
		}
		f.mu.Lock()
		f.byName[d.Name] = tip
		seen[d.Name] = true
		f.mu.Unlock()
	}
	f.mu.Lock()
	for name := range f.byName {
		if !seen[name] {
			delete(f.byName, name)
		}
	}
	f.mu.Unlock()
}

// secret reads a spec's stored PAT so private repos get freshness lines
// too — the token lives inside the deployments grant, so this is the one
// credential the dashboard can legitimately reach.
func (f *freshness) secret(d plystate.Deployment) string {
	ref := d.Field("token_file")
	if ref == "" {
		return ""
	}
	path := ref
	if !filepath.IsAbs(path) {
		path = filepath.Join(f.paths.Deployments, ref)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func (f *freshness) Of(name string) *Freshness {
	f.mu.Lock()
	tip := f.byName[name]
	f.mu.Unlock()
	if tip == nil {
		return nil
	}
	d, ok := plystate.OneDeployment(f.paths, name)
	if !ok || d.Status == nil {
		return nil
	}
	building := strings.HasPrefix(d.Status.Detail, "building")
	if tip.Kind == "commit" {
		cur := d.DeployedCommit()
		return &Freshness{
			Current:         cur,
			Latest:          tip.Latest[:7],
			UpdateAvailable: !building && cur != "" && !strings.HasPrefix(tip.Latest, cur),
			Building:        building,
		}
	}
	cur := d.DeployedVersion()
	return &Freshness{
		Current:         cur,
		Latest:          tip.Latest,
		UpdateAvailable: !building && cur != "" && cur != tip.Latest,
		Building:        building,
	}
}

func main() {
	port := envOr("PORT", "7070")
	dataDir := envOr("DATA_DIR", defaultDataDir())

	// MOCK=true: design mode — a fabricated state tree kept alive by a
	// puppeteer goroutine, no ply CLI needed. Auth and data are sandboxed
	// to a mock dir (login mock/mockmock) so real state is never touched.
	mock := os.Getenv("MOCK") == "true" || os.Getenv("MOCK") == "1"
	if mock {
		dataDir = filepath.Join(os.TempDir(), "ply-dashboard-mock", "data")
		if err := auth.Seed(dataDir, "mock", "mockmock"); err != nil {
			log.Fatalf("mock auth: %v", err)
		}
	}

	a, err := auth.Load(dataDir)
	if err != nil {
		log.Fatalf("auth: %v", err)
	}
	paths := plystate.Resolve()
	if mock {
		paths = plystate.StartMock()
		log.Printf("MOCK — fabricated state at %s, login mock / mockmock", paths.State)
	}
	sampler := plystate.NewSampler(paths, 3*time.Second)
	go sampler.Run()

	fresh := newFreshness(paths)
	go fresh.run()

	s := &server{paths: paths, sampler: sampler, auth: a, registry: registry.NewClient(), fresh: fresh}
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
	mux.HandleFunc("GET /partials/events", s.guard(s.eventsPartial))
	mux.HandleFunc("GET /partials/logpane/{name}", s.guard(s.logPane))
	mux.HandleFunc("GET /partials/termpane/{name}/{n}", s.guard(s.termPane))
	mux.HandleFunc("GET /ws/term/{name}/{n}", s.guard(s.termWS))
	mux.HandleFunc("POST /deploy/{name}/now", s.guard(s.deployNow))
	mux.HandleFunc("POST /deploy/{name}/edit", s.guard(s.deployEdit))
	mux.HandleFunc("POST /deploy/inspect", s.guard(s.sourceInspect))
	mux.HandleFunc("POST /deploy/preview", s.guard(s.sourcePreview))
	mux.HandleFunc("POST /deploy/source", s.guard(s.sourceCreate))

	log.Printf("ply-dashboard %s — listening on :%s (state: %s)", version, port, paths.State)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

// --- template plumbing -------------------------------------------------------

// Each page parses base + its own file + the partials it includes; partials
// also render standalone for htmx swaps.
func (s *server) parseTemplates() {
	funcs := template.FuncMap{
		"statsOf":        s.sampler.Stats,
		"isBuilder":      func(name string) bool { return strings.HasSuffix(name, "-builder") },
		"contract":       registry.Contract,
		"defaultPublish": registry.DefaultPublish,
		"freshOf":        s.fresh.Of,
	}
	page := func(files ...string) *template.Template {
		paths := append([]string{"web/templates/base.html"}, files...)
		return template.Must(template.New("base.html").Funcs(funcs).ParseFS(webFS, paths...))
	}
	s.pages = map[string]*template.Template{
		"index":  page("web/templates/index.html", "web/templates/apps_table.html", "web/templates/events.html"),
		"app":    page("web/templates/app.html", "web/templates/instances.html", "web/templates/logs.html", "web/templates/events.html"),
		"deploy": page("web/templates/deploy.html", "web/templates/deployments.html", "web/templates/deploy_source.html"),
		"login":  page("web/templates/login.html"),
		"setup":  page("web/templates/setup.html"),
		// standalone partials for htmx polling
		"apps_table":    template.Must(template.New("p").Funcs(funcs).ParseFS(webFS, "web/templates/apps_table.html")),
		"instances":     template.Must(template.New("p").Funcs(funcs).ParseFS(webFS, "web/templates/instances.html")),
		"logs":          template.Must(template.New("p").Funcs(funcs).ParseFS(webFS, "web/templates/logs.html")),
		"deployments":   template.Must(template.New("p").Funcs(funcs).ParseFS(webFS, "web/templates/deployments.html")),
		"deploy_source": template.Must(template.New("p").Funcs(funcs).ParseFS(webFS, "web/templates/deploy_source.html")),
		"events":        template.Must(template.New("p").Funcs(funcs).ParseFS(webFS, "web/templates/events.html")),
		"logpane":       template.Must(template.New("p").Funcs(funcs).ParseFS(webFS, "web/templates/logpane.html")),
		"termpane":      template.Must(template.New("p").Funcs(funcs).ParseFS(webFS, "web/templates/termpane.html")),
	}
}

type pageData struct {
	Authed  bool
	Version string
	Section string // which nav item this page lives under
	Error   string

	Apps       []plystate.App
	AppName    string
	App        plystate.App
	Instances  []plystate.Instance
	Commands   []command
	Slot       uint32
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
	Source          *sourceForm
	Events          []plystate.Event
}

// sourceForm is the from-source wizard's state: inspection result plus the
// form exactly as typed, so error re-renders never lose input.
type sourceForm struct {
	RepoURL    string
	Insp       github.Inspection
	Inspected  bool
	Frameworks []string
	Framework  string
	Lane       string // "release" (CI image from GitHub releases) | "source" (build here)
	Token      string
	Spec       plystate.SourceSpec
	Gh         plystate.GithubSpec
	Preview    string
	Error      string
}

var pageSection = map[string]string{"index": "apps", "app": "apps", "deploy": "deploy"}

func (s *server) render(w http.ResponseWriter, page, name string, data pageData) {
	data.Version = version
	data.Section = pageSection[page]
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
	s.render(w, "index", "base.html", pageData{
		Authed: true,
		Apps:   s.apps(),
		Events: plystate.Events(s.paths, "", 25),
	})
}

func (s *server) eventsPartial(w http.ResponseWriter, r *http.Request) {
	app := r.URL.Query().Get("app")
	limit := 25
	if app != "" {
		limit = 15
	}
	s.render(w, "events", "events", pageData{Events: plystate.Events(s.paths, app, limit)})
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
	data.Events = plystate.Events(s.paths, name, 15)
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

var paneName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// logPane: the right drawer — an app's merged log rings, discovered from
// files so it reads dead apps (builder post-mortems) as well as live ones.
func (s *server) logPane(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !paneName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	data := pageData{AppName: name, LogLines: plystate.LogTailAll(s.paths, name, 500)}
	which := "logpane"
	if r.URL.Query().Get("body") == "1" {
		which = "logpane_body"
	}
	s.render(w, "logpane", which, data)
}

// termPane renders the drawer shell; its script opens the websocket.
func (s *server) termPane(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	slot, err := strconv.ParseUint(r.PathValue("n"), 10, 32)
	if !paneName.MatchString(name) || err != nil {
		http.NotFound(w, r)
		return
	}
	s.render(w, "termpane", "termpane", pageData{AppName: name, Slot: uint32(slot)})
}

// termWS: ask the app's run parent for a PTY (control/exec), wait for its
// socket, then bridge. The parent polls on a 2s cadence — the wait budget
// is two beats plus margin.
func (s *server) termWS(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	slot, err := strconv.ParseUint(r.PathValue("n"), 10, 32)
	if !paneName.MatchString(name) || err != nil {
		http.NotFound(w, r)
		return
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		http.Error(w, "entropy", http.StatusInternalServerError)
		return
	}
	hexNonce := hex.EncodeToString(nonce)
	if err := plystate.SubmitExec(s.paths, name, uint32(slot), hexNonce); err != nil {
		http.Error(w, "control dir not writable: "+err.Error(), http.StatusConflict)
		return
	}
	socket := plystate.TermSocket(s.paths, name, hexNonce)
	deadline := time.Now().Add(6 * time.Second)
	for {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			http.Error(w, "no answer from the app's supervisor — it may predate terminals (restart its unit after upgrading ply)", http.StatusGatewayTimeout)
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	if err := term.Bridge(w, r, socket); err != nil {
		log.Printf("terminal %s.%d: %v", name, slot, err)
	}
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
	s.fresh.Kick()
	http.Redirect(w, r, "/deploy", http.StatusSeeOther)
}

// deployNow: the update button — touch the spec, inotify does the rest.
func (s *server) deployNow(w http.ResponseWriter, r *http.Request) {
	if err := plystate.TouchDeployment(s.paths, r.PathValue("name")); err != nil {
		log.Printf("deploy now: %v", err)
	}
	back := r.Header.Get("Referer")
	if back == "" {
		back = "/deploy"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// deployEdit saves the spec exactly as typed — the file is the truth, and
// reconcile is the validator whose verdict lands in the status line.
func (s *server) deployEdit(w http.ResponseWriter, r *http.Request) {
	err := plystate.RewriteDeployment(s.paths, r.PathValue("name"), r.FormValue("spec"))
	if err != nil {
		http.Redirect(w, r, "/deploy?err="+template.URLQueryEscaper(err.Error()), http.StatusSeeOther)
		return
	}
	s.fresh.Kick()
	http.Redirect(w, r, "/deploy", http.StatusSeeOther)
}

func (s *server) deployDelete(w http.ResponseWriter, r *http.Request) {
	if err := plystate.DeleteDeployment(s.paths, r.PathValue("name")); err != nil {
		log.Printf("delete deployment: %v", err)
	}
	s.fresh.Kick()
	http.Redirect(w, r, "/deploy", http.StatusSeeOther)
}

func (s *server) deploymentsPartial(w http.ResponseWriter, _ *http.Request) {
	s.render(w, "deployments", "deployments", pageData{
		Deployments: plystate.Deployments(s.paths),
	})
}

// --- the from-source wizard --------------------------------------------------

func (s *server) renderSource(w http.ResponseWriter, form *sourceForm) {
	form.Frameworks = github.Frameworks()
	s.render(w, "deploy_source", "deploy_source", pageData{Source: form})
}

// sourceInspect answers the inspect button, a lane switch, and a preset
// switch (the latter two arrive with inspected=1 and keep typed fields).
func (s *server) sourceInspect(w http.ResponseWriter, r *http.Request) {
	repoURL := strings.TrimSpace(r.FormValue("repo"))
	token := strings.TrimSpace(r.FormValue("token"))
	form := &sourceForm{RepoURL: repoURL, Token: token}
	if repoURL == "" {
		form.Error = "paste a repo URL first"
		s.renderSource(w, form)
		return
	}
	insp, err := github.Inspect(repoURL, token)
	if err != nil {
		form.Error = err.Error()
		s.renderSource(w, form)
		return
	}
	form.Insp = insp
	form.Inspected = true

	// the lane is a pre-answered question: releases with a ply image beat
	// building on the droplet — but the radio lets you disagree
	form.Lane = "source"
	if insp.Release != nil {
		form.Lane = "release"
	}
	if lane := r.FormValue("lane"); lane == "release" || lane == "source" {
		form.Lane = lane
	}
	form.Framework = insp.Framework
	if fw := r.FormValue("framework"); fw != "" {
		form.Framework = fw
	}

	spec, gh := plystate.SourceSpec{}, plystate.GithubSpec{}
	if r.FormValue("inspected") == "1" {
		spec, gh = specFromForm(r), githubFromForm(r)
	} else {
		name := nameFromRepo(insp.CloneURL)
		spec.Name, gh.Name = name, name
		spec.Ref = insp.DefaultBranch
		spec.Publish, gh.Publish = r.FormValue("publish"), r.FormValue("publish")
	}
	preset := github.PresetFor(form.Framework)
	spec.Build, spec.Runtime = preset.Build, preset.Runtime
	spec.Entrypoint, spec.Include, spec.Port = preset.Entrypoint, preset.Include, preset.Port
	if spec.Publish == "" && preset.Port != "" {
		spec.Publish = "internal:" + preset.Port
	}
	spec.Repo = insp.CloneURL
	gh.Repo = insp.Repo
	if insp.Release != nil && gh.Asset == "" {
		gh.Asset = insp.Release.Asset
	}
	if gh.Name == "" {
		gh.Name = nameFromRepo(insp.CloneURL)
	}
	form.Spec, form.Gh = spec, gh
	form.Preview = previewFor(form)
	s.renderSource(w, form)
}

func (s *server) sourcePreview(w http.ResponseWriter, r *http.Request) {
	text, err := renderFromForm(r)
	if err != nil {
		fmt.Fprintf(w, `<div class="text-amber-400/90">%s</div>`, template.HTMLEscapeString(err.Error()))
		return
	}
	fmt.Fprintf(w, `<pre class="border border-zinc-800 rounded px-3 py-2 text-zinc-400 overflow-x-auto">%s</pre>`, template.HTMLEscapeString(text))
}

func (s *server) sourceCreate(w http.ResponseWriter, r *http.Request) {
	lane := r.FormValue("lane")
	token := strings.TrimSpace(r.FormValue("token"))
	form := &sourceForm{
		RepoURL:   r.FormValue("repo"),
		Inspected: true,
		Lane:      lane,
		Token:     token,
		Framework: r.FormValue("framework"),
		Spec:      specFromForm(r),
		Gh:        githubFromForm(r),
	}
	fail := func(err error) {
		form.Error = err.Error()
		form.Preview = previewFor(form)
		s.renderSource(w, form)
	}
	if _, err := renderFromForm(r); err != nil { // validate before touching disk
		fail(err)
		return
	}
	if lane == "release" {
		gh := form.Gh
		if token != "" {
			ref, err := plystate.WriteToken(s.paths, gh.Name, token)
			if err != nil {
				fail(err)
				return
			}
			gh.TokenFile = ref
		}
		if err := plystate.WriteGithubDeployment(s.paths, gh); err != nil {
			fail(err)
			return
		}
	} else {
		spec := form.Spec
		if token != "" {
			ref, err := plystate.WriteToken(s.paths, spec.Name, token)
			if err != nil {
				fail(err)
				return
			}
			spec.TokenFile = ref
		}
		if err := plystate.WriteSourceDeployment(s.paths, spec); err != nil {
			fail(err)
			return
		}
	}
	s.fresh.Kick()
	w.Header().Set("HX-Redirect", "/deploy")
}

// renderFromForm builds the lane's spec exactly as create would (token
// reference included) — the preview and the written file cannot diverge.
func renderFromForm(r *http.Request) (string, error) {
	token := strings.TrimSpace(r.FormValue("token"))
	if r.FormValue("lane") == "release" {
		gh := githubFromForm(r)
		if token != "" {
			gh.TokenFile = ".keys/" + gh.Name + ".token"
		}
		return gh.Render()
	}
	spec := specFromForm(r)
	if token != "" {
		spec.TokenFile = ".keys/" + spec.Name + ".token"
	}
	return spec.Render()
}

func previewFor(form *sourceForm) string {
	if form.Lane == "release" {
		gh := form.Gh
		if form.Token != "" {
			gh.TokenFile = ".keys/" + gh.Name + ".token"
		}
		text, err := gh.Render()
		if err != nil {
			return ""
		}
		return text
	}
	spec := form.Spec
	if form.Token != "" {
		spec.TokenFile = ".keys/" + spec.Name + ".token"
	}
	text, err := spec.Render()
	if err != nil {
		return ""
	}
	return text
}

func specFromForm(r *http.Request) plystate.SourceSpec {
	return plystate.SourceSpec{
		Name:       strings.TrimSpace(r.FormValue("name")),
		Repo:       strings.TrimSpace(r.FormValue("repo")),
		Ref:        strings.TrimSpace(r.FormValue("ref")),
		Build:      strings.TrimSpace(r.FormValue("build")),
		Runtime:    strings.TrimSpace(r.FormValue("runtime")),
		Entrypoint: strings.TrimSpace(r.FormValue("entrypoint")),
		Include:    strings.TrimSpace(r.FormValue("include")),
		Port:       strings.TrimSpace(r.FormValue("port")),
		Publish:    strings.TrimSpace(r.FormValue("publish")),
		Domain:     strings.TrimSpace(r.FormValue("domain")),
		Env:        r.FormValue("env"),
		Manual:     r.FormValue("manual") == "1",
	}
}

func githubFromForm(r *http.Request) plystate.GithubSpec {
	return plystate.GithubSpec{
		Name:    strings.TrimSpace(r.FormValue("name")),
		Repo:    strings.TrimSpace(r.FormValue("gh_repo")),
		Asset:   strings.TrimSpace(r.FormValue("gh_asset")),
		Version: strings.TrimSpace(r.FormValue("gh_version")),
		Publish: strings.TrimSpace(r.FormValue("publish")),
		Domain:  strings.TrimSpace(r.FormValue("domain")),
		Env:     r.FormValue("env"),
		Manual:  r.FormValue("manual") == "1",
	}
}

var nameSanitize = regexp.MustCompile(`[^a-z0-9-]+`)

func nameFromRepo(repo string) string {
	base := strings.ToLower(path.Base(strings.TrimSuffix(repo, ".git")))
	base = strings.Trim(nameSanitize.ReplaceAllString(base, "-"), "-")
	if base == "" {
		base = "app"
	}
	return base
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
