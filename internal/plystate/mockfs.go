package plystate

// mockfs.go — MOCK=true: fabricate the whole on-disk state surface in a
// temp directory and keep it alive with a puppeteer goroutine. The mock is
// fake files, not fake code paths: every byte the dashboard renders still
// flows through the same parsers production uses, so what you design
// against is what production looks like.

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// StartMock (re)builds the fixture tree and starts the puppeteer.
func StartMock() Paths {
	root := filepath.Join(os.TempDir(), "ply-dashboard-mock", "state-root")
	_ = os.RemoveAll(root) // deterministic cast on every boot
	w := newMockWorld(root)
	go w.run()
	return w.p
}

type mockWorld struct {
	p       Paths
	pidNext int
	// firstSeen: when the puppeteer noticed a status-less deployment —
	// "reconcile" answers ~3s later, like the real inotify+oneshot does.
	firstSeen map[string]time.Time
}

func newMockWorld(root string) *mockWorld {
	w := &mockWorld{
		p: Paths{
			State:       filepath.Join(root, "run/state"),
			Logs:        filepath.Join(root, "run/logs"),
			Apps:        filepath.Join(root, "apps"),
			Deployments: filepath.Join(root, "deployments"),
			Cgroup:      filepath.Join(root, "cgroup"),
			Proc:        filepath.Join(root, "proc"),
		},
		pidNext:   40001,
		firstSeen: map[string]time.Time{},
	}
	for _, dir := range []string{w.p.State, w.p.Logs, w.p.Apps, filepath.Join(w.p.Deployments, ".status"), w.p.Cgroup, w.p.Proc} {
		must(os.MkdirAll(dir, 0o755))
	}

	now := time.Now()
	w.spawn("nextapp", 1, mockApp{port: "web:3000", published: "10.77.0.1:3000", domains: []string{"test.plybox.sh"}, started: now.Add(-73 * time.Hour)})
	w.spawn("nextapp", 2, mockApp{port: "web:3000", started: now.Add(-5 * time.Hour)})
	w.spawn("redis", 1, mockApp{port: "db:6379", version: "8.0.2", started: now.Add(-9 * time.Hour)})
	w.spawn("postgres", 1, mockApp{port: "db:5432", version: "17.10", started: now.Add(-49 * time.Hour)})
	w.spawn("dashboard", 1, mockApp{port: "web:7070", published: "10.77.0.1:7070", domains: []string{"dash.plybox.sh"}, version: "0.1.3", started: now.Add(-1 * time.Hour)})
	// the unhealthy one: instance 1 crash-loops, instance 2 is plain dead
	w.spawn("worker", 1, mockApp{port: "job:9090", restarts: 7, started: now.Add(-47 * time.Second)})
	w.spawn("worker", 2, mockApp{port: "job:9090", restarts: 13, started: now.Add(-6 * time.Hour), dead: true})

	w.seedDeployments()
	// event history so the panel isn't empty on first paint
	w.event(now.Add(-73*time.Hour), "nextapp", "deploy", "deployed nextapp-0.1.0-linux-x64.img @ 6bb7edf")
	w.event(now.Add(-49*time.Hour), "postgres", "deploy", "deployed postgres-17.10-linux-x64.img")
	w.event(now.Add(-9*time.Hour), "redis", "deploy", "deployed redis-8.0.2-linux-x64.img @ 8.0.2")
	w.event(now.Add(-5*time.Hour), "nextapp", "scale", "1 -> 2")
	w.event(now.Add(-95*time.Second), "worker", "deploy-failed", "build failed (exit 101) — `ply logs worker-builder` has the output")
	w.event(now.Add(-47*time.Second), "worker", "instance-restart", "worker.1 respawned (restart #7)")
	// a dead builder's ring: the post-mortem the failed-deploy event links to
	for _, line := range []string{
		"npm warn deprecated glob@7.2.3",
		"added 214 packages in 42s",
		"> worker@0.3.1 build",
		"> cargo build --release",
		"   Compiling worker v0.3.1 (/work)",
		"error[E0308]: mismatched types --> src/consume.rs:47:18",
		"error: could not compile `worker` (bin \"worker\") due to 1 previous error",
	} {
		appendFile(w.logPath("worker-builder", 1), line+"\n")
	}
	return w
}

// event appends one journal line, timestamped in the past for seeds.
func (w *mockWorld) event(at time.Time, app, event, detail string) {
	e := Event{TS: at.Unix(), App: app, Event: event, Detail: detail}
	raw, _ := json.Marshal(e)
	appendFile(filepath.Join(w.p.Apps, "events.log"), string(raw)+"\n")
}

type mockApp struct {
	port      string // "name:port"
	published string
	domains   []string
	version   string // image version; default 0.1.4
	restarts  uint32
	started   time.Time
	dead      bool
}

func (w *mockWorld) spawn(app string, n uint32, cfg mockApp) {
	pid := w.pidNext
	w.pidNext++
	version := cfg.version
	if version == "" {
		version = "0.1.4"
	}
	name, port, _ := strings.Cut(cfg.port, ":")
	portN, _ := strconv.Atoi(port)
	if cfg.started.IsZero() {
		cfg.started = time.Now()
	}
	inst := Instance{
		App:           app,
		N:             n,
		PID:           pid,
		IP:            fmt.Sprintf("10.77.0.%d", pid-40000+1),
		Ports:         map[string]uint16{name: uint16(portN)},
		Image:         fmt.Sprintf("/srv/deploy/%s-%s-linux-x64.img", app, version),
		Started:       cfg.started.Unix(),
		Restarts:      cfg.restarts,
		PublishedAddr: cfg.published,
		Domains:       cfg.domains,
	}
	raw, _ := json.MarshalIndent(inst, "", " ")
	must(os.WriteFile(w.statePath(app, n), raw, 0o644))
	if !cfg.dead {
		must(os.MkdirAll(filepath.Join(w.p.Proc, fmt.Sprint(pid)), 0o755))
	}
	cg := w.cgroupDir(app, n)
	must(os.MkdirAll(cg, 0o755))
	writeFile(filepath.Join(cg, "cpu.stat"), fmt.Sprintf("usage_usec %d\n", 1_000_000_000+rand.IntN(1_000_000_000)))
	writeFile(filepath.Join(cg, "memory.current"), fmt.Sprint(memBase(app)))
	log := w.logPath(app, n)
	for i := 0; i < 25; i++ {
		appendFile(log, logLine(app))
	}
}

func (w *mockWorld) statePath(app string, n uint32) string {
	return filepath.Join(w.p.State, fmt.Sprintf("%s.%d.json", app, n))
}
func (w *mockWorld) logPath(app string, n uint32) string {
	return filepath.Join(w.p.Logs, fmt.Sprintf("%s.%d.log", app, n))
}
func (w *mockWorld) cgroupDir(app string, n uint32) string {
	return filepath.Join(w.p.Cgroup, fmt.Sprintf("ply-%s.%d", app, n))
}

// seedDeployments covers every status the UI can render: ok, failed,
// waiting-forever, and one per CD lane so the spec panel shows them all.
func (w *mockWorld) seedDeployments() {
	specs := map[string]string{
		"redis": `app = "redis"
version = "8.0"
publish = ["internal:6379"]

[env]
REDIS_PASSWORD = "s3cret"
`,
		"nextapp": `repo = "https://github.com/iluxav/next-dummy"
build = "npm ci && npm run build"
runtime = "node@24"
entrypoint = "node .next/standalone/server.js"
port = 3000
publish = ["internal:3000"]
domain = ["test.plybox.sh"]
`,
		"dashboard": `github = "iluxav/ply-dashboard"
publish = ["internal:7070"]
domain = ["dash.plybox.sh"]
`,
		"worker": `repo = "git@github.com:acme/worker.git"
deploy_key = "/etc/ply/keys/worker"
env_file = "/root/worker.env"
build = "cargo build --release"
runtime = "debian@13"
entrypoint = "target/release/worker"
`,
		"blog": `app = "ghost"
publish = ["internal:2368"]
env_file = ".env/plybox.env"
`,
	}
	for name, spec := range specs {
		writeFile(filepath.Join(w.p.Deployments, name+".toml"), spec)
	}
	// a managed env file (referenced by blog) and an external reference,
	// so the env panel shows both rows
	writeFile(filepath.Join(w.p.Deployments, ".env", "plybox.env"), "# shared site secrets\nPOSTGRES_PASSWORD=mock-not-real\nGITHUB_CLIENT_SECRET=mock-not-real\n")
	now := time.Now().Unix()
	w.writeStatus("redis", DeployStatus{OK: true, Detail: "unchanged (redis-8.0.2-linux-x64.img @ 8.0.2)", TS: now - 3600})
	w.writeStatus("nextapp", DeployStatus{OK: true, Detail: "deployed nextapp-0.1.0-linux-x64.img @ 6bb7edf", TS: now - 320})
	// 0.1.1 on purpose: older than the real repo's latest release, so the
	// freshness checker paints the "update available" state in mock mode
	w.writeStatus("dashboard", DeployStatus{OK: true, Detail: "unchanged (dashboard-0.1.1-linux-x64.img)", TS: now - 7200})
	w.writeStatus("worker", DeployStatus{OK: false, Detail: "build failed (exit 101) — `ply logs worker-builder` has the output", TS: now - 95})
	// blog gets no status on purpose: the forever-"waiting for reconcile…" row
}

func (w *mockWorld) writeStatus(name string, st DeployStatus) {
	raw, _ := json.Marshal(st)
	writeFile(filepath.Join(w.p.Deployments, ".status", name+".status"), string(raw))
}

// --- the puppeteer -----------------------------------------------------------

// One serialized loop, 1s heartbeat: no locks, no races. Sub-rhythms pick
// their own beats off the tick counter.
func (w *mockWorld) run() {
	tick := 0
	for range time.Tick(time.Second) {
		tick++
		w.answerControls()
		w.playReconciler()
		if tick%2 == 0 {
			w.breathe()
		}
		if tick%17 == 0 {
			w.crashWorker()
		}
	}
}

// breathe: logs scroll, sparklines move.
func (w *mockWorld) breathe() {
	instances, _ := List(w.p)
	for _, inst := range instances {
		if !inst.Alive {
			continue
		}
		if rand.IntN(3) > 0 {
			appendFile(w.logPath(inst.App, inst.N), logLine(inst.App))
		}
		cg := w.cgroupDir(inst.App, inst.N)
		usec := readKeyed(filepath.Join(cg, "cpu.stat"), "usage_usec")
		writeFile(filepath.Join(cg, "cpu.stat"), fmt.Sprintf("usage_usec %d\n", usec+cpuDelta(inst.App)))
		mem := readUint(filepath.Join(cg, "memory.current"))
		writeFile(filepath.Join(cg, "memory.current"), fmt.Sprint(wander(mem, memBase(inst.App))))
	}
}

// answerControls plays the run parent: consume command files within ~1s,
// leave a last-result line, make the fixture match.
func (w *mockWorld) answerControls() {
	apps, err := os.ReadDir(w.p.Apps)
	if err != nil {
		return
	}
	for _, e := range apps {
		app := e.Name()
		dir := filepath.Join(w.p.Apps, app, "control")
		if raw, err := os.ReadFile(filepath.Join(dir, "scale")); err == nil {
			_ = os.Remove(filepath.Join(dir, "scale"))
			n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
			if err != nil || n < 1 {
				w.result(app, CommandResult{Command: "scale", OK: false, Detail: "bad count", TS: time.Now().Unix()})
				continue
			}
			w.scaleTo(app, uint32(n))
			w.result(app, CommandResult{Command: "scale", OK: true, Detail: fmt.Sprintf("scaled to %d", n), TS: time.Now().Unix()})
			w.event(time.Now(), app, "scale", fmt.Sprintf("-> %d", n))
		}
		if _, err := os.Stat(filepath.Join(dir, "restart")); err == nil {
			_ = os.Remove(filepath.Join(dir, "restart"))
			rolled := w.restartAll(app)
			w.result(app, CommandResult{Command: "restart", OK: true, Detail: fmt.Sprintf("rolled %d instances", rolled), TS: time.Now().Unix()})
			w.event(time.Now(), app, "restart", fmt.Sprintf("rolling restart (%d slots)", rolled))
		}
	}
}

func (w *mockWorld) result(app string, r CommandResult) {
	raw, _ := json.Marshal(r)
	dir := filepath.Join(w.p.Apps, app, "control")
	_ = os.MkdirAll(dir, 0o755)
	writeFile(filepath.Join(dir, "last-result"), string(raw))
}

func (w *mockWorld) scaleTo(app string, want uint32) {
	instances, _ := List(w.p)
	var have []Instance
	for _, inst := range instances {
		if inst.App == app {
			have = append(have, inst)
		}
	}
	for n := uint32(len(have)) + 1; n <= want; n++ {
		tpl := mockApp{port: "web:8080"}
		if len(have) > 0 {
			for name, p := range have[0].Ports {
				tpl.port = fmt.Sprintf("%s:%d", name, p)
			}
		}
		w.spawn(app, n, tpl)
	}
	for i := int(want); i < len(have); i++ {
		inst := have[i]
		_ = os.Remove(w.statePath(inst.App, inst.N))
		_ = os.RemoveAll(filepath.Join(w.p.Proc, fmt.Sprint(inst.PID)))
		_ = os.RemoveAll(w.cgroupDir(inst.App, inst.N))
		_ = os.Remove(w.logPath(inst.App, inst.N))
		_ = os.Remove(w.logPath(inst.App, inst.N) + ".1")
	}
}

func (w *mockWorld) restartAll(app string) int {
	instances, _ := List(w.p)
	rolled := 0
	for _, inst := range instances {
		if inst.App != app {
			continue
		}
		inst.Started = time.Now().Unix()
		raw, _ := json.MarshalIndent(inst, "", " ")
		writeFile(w.statePath(inst.App, inst.N), string(raw))
		appendFile(w.logPath(inst.App, inst.N), fmt.Sprintf("%s | rolling restart: new instance up\n", stamp()))
		rolled++
	}
	return rolled
}

// playReconciler answers new status-less deployments ~3s after they appear —
// except "blog", kept forever-waiting so that UI state stays on screen.
func (w *mockWorld) playReconciler() {
	entries, err := os.ReadDir(w.p.Deployments)
	if err != nil {
		return
	}
	for _, e := range entries {
		name, found := strings.CutSuffix(e.Name(), ".toml")
		if !found || strings.HasPrefix(name, ".") || name == "blog" {
			continue
		}
		if _, err := os.Stat(filepath.Join(w.p.Deployments, ".status", name+".status")); err == nil {
			continue
		}
		seen, ok := w.firstSeen[name]
		if !ok {
			w.firstSeen[name] = time.Now()
			continue
		}
		if time.Since(seen) < 3*time.Second {
			continue
		}
		delete(w.firstSeen, name)
		detail := fmt.Sprintf("deployed %s-0.1.0-linux-x64.img @ %07x", name, rand.IntN(0xfffffff))
		w.writeStatus(name, DeployStatus{OK: true, Detail: detail, TS: time.Now().Unix()})
		w.event(time.Now(), name, "deploy", detail)
		if instances, _ := List(w.p); !hasApp(instances, name) {
			w.spawn(name, 1, mockApp{port: "web:8080", started: time.Now()})
		}
	}
}

// crashWorker keeps worker.1 visibly unhealthy: panic in the log, restart
// counter up, uptime back to zero.
func (w *mockWorld) crashWorker() {
	raw, err := os.ReadFile(w.statePath("worker", 1))
	if err != nil {
		return
	}
	var inst Instance
	if json.Unmarshal(raw, &inst) != nil {
		return
	}
	inst.Restarts++
	inst.Started = time.Now().Unix()
	out, _ := json.MarshalIndent(inst, "", " ")
	writeFile(w.statePath("worker", 1), string(out))
	log := w.logPath("worker", 1)
	appendFile(log, fmt.Sprintf("%s | panic: connection reset by peer (queue upstream)\n", stamp()))
	appendFile(log, fmt.Sprintf("%s | goroutine 1 [running]: main.consume(0xc000112000)\n", stamp()))
	appendFile(log, fmt.Sprintf("%s | worker starting (attempt %d) — connecting to queue…\n", stamp(), inst.Restarts+1))
	w.event(time.Now(), "worker", "instance-restart", fmt.Sprintf("worker.1 respawned (restart #%d)", inst.Restarts))
}

func hasApp(instances []Instance, app string) bool {
	for _, inst := range instances {
		if inst.App == app {
			return true
		}
	}
	return false
}

// --- texture -----------------------------------------------------------------

func memBase(app string) uint64 {
	switch app {
	case "nextapp":
		return 182 * 1024 * 1024
	case "postgres":
		return 96 * 1024 * 1024
	case "dashboard":
		return 14 * 1024 * 1024
	case "worker":
		return 44 * 1024 * 1024
	default:
		return 9 * 1024 * 1024
	}
}

// cpuDelta: microseconds burned per 2s beat — nextapp busy, redis idle,
// postgres spiky.
func cpuDelta(app string) uint64 {
	switch app {
	case "nextapp":
		return uint64(40_000 + rand.IntN(140_000))
	case "postgres":
		if rand.IntN(6) == 0 {
			return uint64(200_000 + rand.IntN(400_000))
		}
		return uint64(5_000 + rand.IntN(20_000))
	case "worker":
		return uint64(rand.IntN(300_000))
	default:
		return uint64(1_000 + rand.IntN(9_000))
	}
}

// wander: random walk with a soft pull back toward base (±25% band).
func wander(current, base uint64) uint64 {
	if current == 0 {
		return base
	}
	step := int64(base / 40)
	next := int64(current) + rand.Int64N(2*step+1) - step
	if next > int64(base)*5/4 {
		next = int64(base) * 5 / 4
	}
	if next < int64(base)*3/4 {
		next = int64(base) * 3 / 4
	}
	return uint64(next)
}

var logFlavor = map[string][]string{
	"nextapp": {
		`GET / 200 in 14ms`,
		`GET /api/posts 200 in 38ms`,
		`GET /_next/static/chunks/main.js 200 in 2ms`,
		`POST /api/subscribe 201 in 91ms`,
		`GET /pricing 200 in 11ms`,
	},
	"redis": {
		`* 1 changes in 3600 seconds. Saving...`,
		`* Background saving terminated with success`,
		`* DB saved on disk`,
	},
	"postgres": {
		`LOG:  checkpoint starting: time`,
		`LOG:  checkpoint complete: wrote 12 buffers (0.1%)`,
		`LOG:  automatic vacuum of table "app.public.events"`,
	},
	"dashboard": {
		`GET /partials/apps 200`,
		`GET /partials/logs/nextapp 200`,
		`GET /deploy 200`,
	},
	"worker": {
		`consumed job 8471 (emails.welcome) in 120ms`,
		`consumed job 8472 (emails.digest) in 340ms`,
		`queue depth: 3`,
	},
}

func logLine(app string) string {
	flavor := logFlavor[app]
	if len(flavor) == 0 {
		flavor = []string{"ready — listening on :8080", "request handled in 9ms"}
	}
	return fmt.Sprintf("%s | %s\n", stamp(), flavor[rand.IntN(len(flavor))])
}

func stamp() string { return time.Now().Format("15:04:05") }

func writeFile(path, content string) {
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(content), 0o644)
}

func appendFile(path, line string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}

func must(err error) {
	if err != nil {
		panic(fmt.Sprintf("mock fixture: %v", err))
	}
}
