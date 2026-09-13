package main

import (
	"crypto/rand"
	"encoding/hex"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/iluxav/ply-dashboard/internal/cart"
	"github.com/iluxav/ply-dashboard/internal/github"
	"github.com/iluxav/ply-dashboard/internal/plystate"
)

// --- template helpers (registered in parseTemplates) -------------------------

// srcIconPaths are the inner Lucide SVG paths per source kind — the SAME
// glyphs the omnibox swaps client-side, so a card's badge and the live input
// icon read as one language.
var srcIconPaths = map[string]string{
	"repo":     `<line x1="6" x2="6" y1="3" y2="15"/><circle cx="18" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><path d="M18 9a9 9 0 0 1-9 9"/>`,
	"registry": `<path d="m7.5 4.27 9 5.15"/><path d="M21 8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16Z"/><path d="m3.3 7 8.7 5 8.7-5"/><path d="M12 22V12"/>`,
	"image":    `<line x1="22" x2="2" y1="12" y2="12"/><path d="M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z"/><line x1="6" x2="6.01" y1="16" y2="16"/><line x1="10" x2="10.01" y1="16" y2="16"/>`,
	"docker":   `<path d="M21 8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16Z"/><path d="m3.3 7 8.7 5 8.7-5"/><path d="M12 22V12"/>`,
	"search":   `<circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/>`,
}

func srcIconSVG(kind string) template.HTML {
	p, ok := srcIconPaths[kind]
	if !ok {
		p = srcIconPaths["search"]
	}
	return template.HTML(`<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">` + p + `</svg>`)
}

func srcBadge(kind string) string {
	switch kind {
	case cart.KindRepo:
		return "git+"
	case cart.KindImage:
		return "image"
	default:
		return "registry"
	}
}

func inc(i int) int { return i + 1 }

func hasStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// The deploy BUILDER — one flow that assembles a deployment of 1..N services
// (the "cart"), writes it as a draft, and promotes it on deploy. Additive:
// the old /deploy tabs stay until the cutover phase.

// --- view models -------------------------------------------------------------

// otherRef is one of a card's peers — its name plus whether it's a database
// (so the picker can emphasize url/password and the DATABASE_URL one-click can
// appear).
type otherRef struct {
	Name string
	IsDB bool
}

// afterHint names what ply injects for free when this card starts after a peer:
// <PREFIX>_HOST / <PREFIX>_PORT / <PREFIX>_ADDR at runtime, no env needed.
type afterHint struct {
	Name   string
	Prefix string
}

type cardView struct {
	Index       int
	C           cart.Card
	Others      []otherRef  // peers — for the `after` checkboxes and the connect picker
	AfterInject []afterHint // this card's checked afters → the env prefixes ply injects
	DBs         []string    // peer databases — for the DATABASE_URL one-click
	Expects     []string    // env-var keys this app reads (from its .env.example)
	EnvText     string      // env as KEY=VALUE lines
	PublishText string      // publish, one per line
	DomainText  string
	VolumeText  string
}

// isDatabase reports whether a service (by its registry ref or its name) is a
// database ply can compose a connection URL for — drives the DATABASE_URL
// one-click and the picker's field emphasis.
func isDatabase(refOrName string) bool {
	s := strings.ToLower(refOrName)
	for _, kw := range []string{"postgresql", "postgres", "mariadb", "mysql", "valkey", "redis", "mongodb", "mongo"} {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

// injectPrefix is the env prefix `after` injects for a member — its name
// upper-snaked (qa-server → QA_SERVER, giving QA_SERVER_HOST / QA_SERVER_PORT).
func injectPrefix(name string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(name) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// addEnvRef appends a `KEY={service.field}` reference to a card's env — the
// picker's insert. A blank key/service/field is a no-op.
// setEnvKey sets KEY=val, replacing an existing KEY= line rather than
// appending a duplicate — so re-inserting (or clicking a one-click twice) is
// idempotent and updates in place instead of piling up.
func setEnvKey(env []string, key, val string) []string {
	line := key + "=" + val
	for i, e := range env {
		if k, _, ok := strings.Cut(e, "="); ok && strings.TrimSpace(k) == key {
			env[i] = line
			return env
		}
	}
	return append(env, line)
}

func addEnvRef(c *cart.Card, service, field, key string) {
	service, field, key = strings.TrimSpace(service), strings.TrimSpace(field), strings.TrimSpace(key)
	if service == "" || field == "" || key == "" {
		return
	}
	c.Env = setEnvKey(c.Env, key, "{"+service+"."+field+"}")
}

// useDatabase folds the "needs a database?" wizard into a card: inject the
// standard DATABASE_URL={db.url} and make it start after the db (start order +
// address injection). Idempotent on `after`.
func useDatabase(c *cart.Card, db string) {
	db = strings.TrimSpace(db)
	if db == "" {
		return
	}
	c.Env = setEnvKey(c.Env, "DATABASE_URL", "{"+db+".url}")
	if !hasStr(c.After, db) {
		c.After = append(c.After, db)
	}
}

type registryMatch struct {
	Ref         string
	Versions    []string
	Description string
}

type detectedView struct {
	Kind    string // repo | registry | image | docker | empty | error
	Input   string
	Note    string
	Token   string
	Card    cart.Card       // repo/image: the prefilled card to add
	Matches []registryMatch // registry
}

// --- classification (mirrors the client-side omnibox JS) ---------------------

func classifySource(in string) string {
	in = strings.TrimSpace(in)
	switch {
	case in == "":
		return "empty"
	case strings.HasPrefix(in, "docker://"):
		return "docker"
	case strings.HasSuffix(in, ".img"):
		return "image"
	case strings.Contains(in, "github.com"), strings.HasPrefix(in, "git@"), strings.HasSuffix(in, ".git"):
		return "repo"
	case strings.HasPrefix(in, "http://"), strings.HasPrefix(in, "https://"):
		return "repo"
	default:
		// a bare token: registry name unless it looks like a host/path
		host := in
		if i := strings.IndexByte(in, '/'); i >= 0 {
			host = in[:i]
		}
		if strings.Contains(host, ".") {
			return "repo"
		}
		return "registry"
	}
}

// cardFromInspection prefills a repo card from a github inspection — the same
// framework detection the old source wizard used.
func cardFromInspection(insp github.Inspection, input string) cart.Card {
	ref := insp.CloneURL
	if ref == "" {
		ref = input
	}
	c := cart.Card{Kind: cart.KindRepo, Ref: ref, Name: cart.DeriveName(ref)}
	preset := github.PresetFor(insp.Framework)
	c.Build, c.Runtime = preset.Build, preset.Runtime
	switch insp.Framework {
	case "nextjs":
		c.Build = github.NextjsBuild
		c.Publish = []string{"internal:3000"}
	case "ply":
		if insp.AppPort != "" {
			c.Publish = []string{"internal:" + insp.AppPort}
		}
	default:
		if preset.Port != "" {
			c.Publish = []string{"internal:" + preset.Port}
		}
	}
	return c
}

// --- helpers -----------------------------------------------------------------

func newDraftID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "d" + hex.EncodeToString(b)
}

func splitLines(s string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' }) {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (s *server) depDir() string { return s.paths.Deployments }

func toCardView(c cart.Card, i int, all []cart.Card, meta cart.DraftMeta) cardView {
	others := make([]otherRef, 0, len(all))
	var dbs []string
	for j, o := range all {
		if j == i || o.Name == "" {
			continue
		}
		db := isDatabase(o.Ref) || isDatabase(o.Name)
		others = append(others, otherRef{Name: o.Name, IsDB: db})
		if db {
			dbs = append(dbs, o.Name)
		}
	}
	hints := make([]afterHint, 0, len(c.After))
	for _, a := range c.After {
		hints = append(hints, afterHint{Name: a, Prefix: injectPrefix(a)})
	}
	return cardView{
		Index: i, C: c, Others: others, AfterInject: hints, DBs: dbs,
		Expects:     meta[c.Ref],
		EnvText:     strings.Join(c.Env, "\n"),
		PublishText: strings.Join(c.Publish, "\n"),
		DomainText:  strings.Join(c.Domain, "\n"),
		VolumeText:  strings.Join(c.Volume, "\n"),
	}
}

func cardViews(cs []cart.Card, meta cart.DraftMeta) []cardView {
	out := make([]cardView, len(cs))
	for i, c := range cs {
		out[i] = toCardView(c, i, cs, meta)
	}
	return out
}

// parseCardForm reads a card's fields out of an add/edit form.
func parseCardForm(r *http.Request) cart.Card {
	_ = r.ParseForm()
	c := cart.Card{
		Kind:    r.FormValue("kind"),
		Ref:     strings.TrimSpace(r.FormValue("ref")),
		Name:    cart.SafeName(r.FormValue("name")),
		Build:   strings.TrimSpace(r.FormValue("build")),
		Runtime: strings.TrimSpace(r.FormValue("runtime")),
		Publish: splitLines(r.FormValue("publish")),
		Domain:  splitLines(r.FormValue("domain")),
		Env:     splitLines(r.FormValue("env")),
		Volume:  splitLines(r.FormValue("volume")),
		After:   r.Form["after"],
	}
	// registry adds send a base ref + a chosen version separately
	if tmpl := strings.TrimSpace(r.FormValue("reftmpl")); tmpl != "" {
		c.Kind, c.Ref = cart.KindRegistry, tmpl
		if v := strings.TrimSpace(r.FormValue("version")); v != "" {
			c.Ref = tmpl + "@" + v
		}
	}
	if c.Name == "" {
		c.Name = cart.DeriveName(c.Ref)
	}
	return c
}

func (s *server) builderData(draftID string, c cart.Cart) pageData {
	meta := cart.ReadMeta(s.depDir(), draftID)
	return pageData{
		Authed:          true,
		DeployAvailable: plystate.DeploymentsAvailable(s.paths),
		DraftID:         draftID,
		CartName:        c.Name,
		Cards:           cardViews(c.Cards, meta),
	}
}

// --- handlers ----------------------------------------------------------------

// GET /deploy/new — the empty builder (or a reopened draft via ?draft=).
func (s *server) deployNewPage(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("draft")
	if id == "" {
		id = newDraftID()
	}
	c, _ := cart.ReadDraft(s.depDir(), id)
	s.render(w, "deploy_new", "base.html", s.builderData(id, c))
}

// POST /deploy/detect — classify the omnibox input and preview what to add.
func (s *server) deployDetect(w http.ResponseWriter, r *http.Request) {
	in := strings.TrimSpace(r.FormValue("input"))
	token := strings.TrimSpace(r.FormValue("token"))
	id := cart.SafeName(r.FormValue("draft"))
	d := detectedView{Kind: classifySource(in), Input: in, Token: token}
	switch d.Kind {
	case "repo":
		insp, err := github.Inspect(in, token)
		if err != nil {
			d.Kind, d.Note = "error", err.Error()
			break
		}
		d.Note = insp.Note
		d.Card = cardFromInspection(insp, in)
		// remember what the app declares it reads (.env.example), keyed by the
		// card's ref so it survives rename/reorder; the card view shows it.
		if len(insp.EnvExample) > 0 {
			m := cart.ReadMeta(s.depDir(), id)
			m[d.Card.Ref] = insp.EnvExample
			_ = cart.WriteMeta(s.depDir(), id, m)
		}
	case "registry":
		apps, err := s.registry.Apps()
		if err != nil {
			d.Kind, d.Note = "error", err.Error()
			break
		}
		needle := strings.ToLower(in)
		for _, a := range apps {
			if strings.Contains(strings.ToLower(a.Ref), needle) || strings.Contains(strings.ToLower(a.Name), needle) {
				d.Matches = append(d.Matches, registryMatch{Ref: a.Ref, Versions: a.Versions, Description: a.Description})
				if len(d.Matches) >= 8 {
					break
				}
			}
		}
		if len(d.Matches) == 0 {
			d.Note = "no registry package matches “" + in + "” — try a repo URL, or a different name"
		}
	case "image":
		d.Card = cart.Card{Kind: cart.KindImage, Ref: in, Name: cart.DeriveName(in)}
	case "docker":
		d.Note = "Docker images aren't a deployment source yet — `ply import` it and publish, then add it from the registry"
	}
	s.render(w, "detected", "detected", pageData{DraftID: id, Detected: &d})
}

// POST /deploy/draft/{id}/add — append a card, autosave, return the cart.
func (s *server) deployCartAdd(w http.ResponseWriter, r *http.Request) {
	id := cart.SafeName(r.PathValue("id"))
	c, _ := cart.ReadDraft(s.depDir(), id)
	card := parseCardForm(r)
	if card.Ref == "" || card.Kind == "" {
		s.render(w, "cart", "cart", s.builderData(id, c))
		return
	}
	// keep member names unique
	card.Name = uniqueName(card.Name, c.Cards)
	// seed after: the previous card, so a fresh stack starts in order
	if len(c.Cards) > 0 && card.Kind == cart.KindRepo && len(card.After) == 0 {
		card.After = []string{c.Cards[len(c.Cards)-1].Name}
	}
	c.Cards = append(c.Cards, card)
	if c.Name == "" {
		c.Name = card.Name
	}
	_ = cart.WriteDraft(s.depDir(), id, c)
	s.render(w, "cart", "cart", s.builderData(id, c))
}

// POST /deploy/draft/{id}/card/{i} — op=edit|remove|up|down.
func (s *server) deployCardOp(w http.ResponseWriter, r *http.Request) {
	id := cart.SafeName(r.PathValue("id"))
	c, _ := cart.ReadDraft(s.depDir(), id)
	i, _ := strconv.Atoi(r.PathValue("i"))
	if i < 0 || i >= len(c.Cards) {
		s.render(w, "cart", "cart", s.builderData(id, c))
		return
	}
	switch r.FormValue("op") {
	case "remove":
		c.Cards = append(c.Cards[:i], c.Cards[i+1:]...)
	case "up":
		if i > 0 {
			c.Cards[i-1], c.Cards[i] = c.Cards[i], c.Cards[i-1]
		}
	case "down":
		if i < len(c.Cards)-1 {
			c.Cards[i+1], c.Cards[i] = c.Cards[i], c.Cards[i+1]
		}
	case "addref": // the connect picker: append KEY={service.field}
		addEnvRef(&c.Cards[i], r.FormValue("service"), r.FormValue("field"), r.FormValue("key"))
	case "usedb": // the DATABASE_URL one-click
		useDatabase(&c.Cards[i], r.FormValue("db"))
	default: // edit
		edited := parseCardForm(r)
		edited.Kind, edited.Ref = c.Cards[i].Kind, c.Cards[i].Ref // source is immutable
		edited.Name = uniqueNameExcept(edited.Name, c.Cards, i)
		c.Cards[i] = edited
	}
	_ = cart.WriteDraft(s.depDir(), id, c)
	s.render(w, "cart", "cart", s.builderData(id, c))
}

// POST /deploy/draft/{id}/name — autosave the deployment name.
func (s *server) deployCartName(w http.ResponseWriter, r *http.Request) {
	id := cart.SafeName(r.PathValue("id"))
	c, _ := cart.ReadDraft(s.depDir(), id)
	c.Name = cart.SafeName(r.FormValue("name"))
	_ = cart.WriteDraft(s.depDir(), id, c)
	w.WriteHeader(http.StatusNoContent)
}

// GET /deploy/draft/{id}/recipe — the assembled TOML, read-only.
func (s *server) deployCartRecipe(w http.ResponseWriter, r *http.Request) {
	id := cart.SafeName(r.PathValue("id"))
	c, _ := cart.ReadDraft(s.depDir(), id)
	s.render(w, "recipe", "recipe", pageData{RecipeTOML: c.ToTOML()})
}

// POST /deploy/draft/{id}/deploy — assemble, promote, go to the host list.
func (s *server) deployCartDeploy(w http.ResponseWriter, r *http.Request) {
	id := cart.SafeName(r.PathValue("id"))
	c, _ := cart.ReadDraft(s.depDir(), id)
	if n := cart.SafeName(r.FormValue("name")); n != "" {
		c.Name = n
	}
	if err := cart.Deploy(s.depDir(), id, c.Name, c); err != nil {
		http.Redirect(w, r, "/deploy/new?draft="+id+"&err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/deploy", http.StatusSeeOther)
}

// --- name uniqueness ---------------------------------------------------------

func uniqueName(name string, existing []cart.Card) string {
	return uniqueNameExcept(name, existing, -1)
}

func uniqueNameExcept(name string, existing []cart.Card, skip int) string {
	if name == "" {
		name = "svc"
	}
	taken := func(n string) bool {
		for j, e := range existing {
			if j != skip && e.Name == n {
				return true
			}
		}
		return false
	}
	if !taken(name) {
		return name
	}
	for k := 2; ; k++ {
		cand := name + "-" + strconv.Itoa(k)
		if !taken(cand) {
			return cand
		}
	}
}
