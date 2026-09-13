# Deploy, rebuilt: the stack cart

> **For Claude:** This replaces the entire deployment-creation UI. Implement with
> superpowers:executing-plans, task-by-task, committing per task.

**Goal:** One intuitive flow to stand up a deployment of **one or many services**
— paste a repo, search the registry, add an image; wire them; deploy. Kill the
confusing three-source page. This is the surface ply is adopted or dropped on.

**Architecture:** A deployment is a file. The builder assembles a composition
`ply.toml` (`[package]` + `[[service]]`) as a **draft file**, then promotes it
into the deployments dir where `reconcile` builds and wires it. No new runtime —
`reconcile` already expands a `[[service]]` deployment file into one unit per
member (`git+` built on host, registry pulled), so this is a UI-assembly layer.

**Tech:** Go net/http + html/template + Tailwind (play CDN), htmx for partials —
the dashboard's existing stack. Terminal-dark aesthetic, monospace, orange accent.

---

## Principles

1. **One flow, 1..N services.** A single app is a cart of one. There is no
   "single vs stack" choice to make up front.
2. **You type, we detect.** No "source type" tabs. One omnibox figures out
   repo vs registry vs image from what you paste.
3. **The file is the truth.** The builder writes a composition `.toml`; a raw
   view/edit is always one click away. Nothing the UI does is un-inspectable.
4. **Nothing half-deployed.** The cart is a *draft* file outside the watched
   dir; only "Deploy" promotes it. Reconcile never sees a partial stack.
5. **Create == edit.** Opening an existing deployment loads the same builder,
   parsed from its file. One UI, two entrypoints.
6. **Suggest, don't impose.** Wiring (`after`, a sealed db password, injected
   `DATABASE_URL`) is auto-offered and fully editable.

## The core insight

`reconcile.rs` already treats a deployment **file** that is a `[[service]]`
composition as a stack — building each member (a `git+` on the host, a
`postgres@17` from the registry) and wiring them. So the cart's "Deploy" just
writes this and moves it into place:

```toml
[package]
name = "shop"
version = "0.1.0"

[[service]]
run   = "git+https://github.com/you/server"   # add from source
build = "npm install"
after = ["db"]
publish = ["internal:3001"]
env = ["DATABASE_URL={db.url}"]

[[service]]
run  = "postgres@17"                            # add from registry
name = "db"
publish = ["internal:5432"]
env = ["POSTGRES_PASSWORD=enc:v1:…"]            # generated + sealed at wire time

[[service]]
run   = "git+https://github.com/you/web"        # add from source
build = "npm install"
after = ["server"]
publish = ["8080:3000"]
domain = ["shop.example.com"]
```

## The flow

### 1. Deploy page — the list + the entry

```
 ply   apps   deploy   notify                                        logout
 ─────────────────────────────────────────────────────────────────────────
  DEPLOYMENTS ON THIS HOST                              [ + new deployment ]

  ● shop        stack · 3 services   ok           edit  ·  ⋯
  ● dashboard   single               ok · 0.1.44  edit  ·  ⋯

  ─── env files ─────────────────────────────────────────────  [ manage ]
```

Clean list. Each row: health dot, name, kind (single / stack · N services),
status, and an **edit** that opens the builder. `+ new deployment` opens the
empty builder. Env files and secrets move to their own quiet section.

### 2. The builder — the omnibox + the cart

```
 new deployment                                                  [ ✕ close ]
 ───────────────────────────────────────────────────────────────────────────
  ADD A SERVICE
  ┌───────────────────────────────────────────────────────────────────────┐
  │  paste a repo URL, or search the registry (postgres, redis…)      [→]  │
  └───────────────────────────────────────────────────────────────────────┘
     ↳ github.com/you/server  →  GitHub repo · builds on this host   [ add ]
       (or, typing "postgres": a live dropdown of registry matches)

  IN THIS DEPLOYMENT  (3)                                    name:  [ shop ]
  ┌─ 1  server ───────────────────── git+ · github.com/you/server ─ ✎  ✕ ─┐
  │    build    npm install                              (detected)        │
  │    publish  internal:3001                                              │
  ├─ 2  db ─────────────────────────────────── registry · postgres@17 ────┤
  │    publish  internal:5432          data volume: pgdata  ✓              │
  ├─ 3  web ───────────────────────── git+ · github.com/you/web ──────────┤
  │    build    npm install     publish  8080:3000     domain  [        ]  │
  └───────────────────────────────────────────────────────────────────────┘
     ↕ drag a card to set start order

                                          [ wire → ]   [ raw ]   [ deploy ]
```

**The omnibox is the whole trick.** As you type it classifies:
- `github.com/…`, a git URL, `git@…`, `…​.git` → **GitHub repo** → on `add`,
  it inspects (reuse `sourceInspect`): framework, build cmd, publish port
  prefilled into the new card ("Next.js detected — deploys as-is").
- a bare name (`postgres`, `redis`) → **registry search** → a dropdown of
  matches with versions (reuse the registry search); pick one → a card.
- `…​.img` (path or URL) → **image**.
- `docker://…` → a note: import it first (guide; future first-class).

Each card is editable inline; the source badge shows what it is; drag sets the
default `after` chain. A one-service cart is a normal single deploy — no stack
overhead (on deploy it collapses to a plain `repo=` / `app=` / `image=` order).

### 3. Wire (appears once it helps — ≥2 services, or a db present)

```
  WIRE                                                        [ ← back to cart ]
  ───────────────────────────────────────────────────────────────────────────
  CONNECTIONS   (start order + injected addresses)
    db                                                    exposed: internal
    server   ── after ──▶ db     injects DB_HOST, DB_PORT     ✓ auto
    web      ── after ──▶ server injects SERVER_HOST/PORT     ✓ auto

  DATABASE   db is a postgres → server needs credentials
    ● generate a password, seal it for this host, inject as         ✓
      POSTGRES_PASSWORD (db) + DATABASE_URL (server)      [ edit names ]

  PUBLIC EDGE   which service faces the internet?
    ● web    domain [ shop.example.com ]   → :8080 + Caddy TLS
      server, db stay internal

                                                     [ review → ]   [ deploy ]
```

Everything here is **suggested and editable**. The db-detected credential flow
generalizes the existing "needs a database?" wizard: generate → `ply secret
seal` for this host → inject. Toggling any suggestion off drops the line.

### 4. Review → deploy

```
  REVIEW                                             deploy as:  [ shop ]
  ───────────────────────────────────────────────────────────────────────
  the file this writes (the truth):

    [package]
    name = "shop"
    …the assembled composition, read-only…

                              [ ← edit ]        [ deploy · reconcile applies it ]
```

Reuses the read-only recipe `<pre>` we shipped. **Deploy** writes
`deployments/.drafts/shop.toml`, then atomically renames it to
`deployments/shop.toml` — reconcile picks it up on the next inotify event.
Then the **deploying** state (the existing spinner), then **deployed** → a link
to the now-grouped stack on the apps page.

### States

`empty` → `inspecting` (spinner while a URL is fetched) → `card added` →
(repeat) → `wiring` → `review` → `deploying` → `deployed` / `failed` (status
line names the reason, card stays editable). Draft autosaves on every change so
a refresh never loses the cart.

## Data model & file lifecycle

- **Draft:** `deployments/.drafts/<name>.toml` — a dot-dir reconcile ignores
  (same class as `.status/`). Written on every cart mutation (autosave).
- **Promote:** Deploy = `rename(.drafts/<name>.toml, <name>.toml)` (atomic).
  Delete the draft after.
- **Edit existing:** parse `<name>.toml` → cart cards. A composition parses into
  N cards; a single-source order (`repo=`/`app=`/`image=`) into one card. Raw
  toggle shows/edits the file directly (the escape hatch for anything the
  builder can't model).
- **Cart card (server-side model):** `{ index, name, source: {kind: repo|registry|image, ref}, build, runtime, publish[], domain[], env[], after[], volume[] }`. Marshals to a `[[service]]` block; a one-card cart marshals to a flat order.

## Backend (Go)

New/changed handlers (replace the source-lane set):

| route | does |
|---|---|
| `GET  /deploy` | the list + entry (rewritten `deployments.html`) |
| `GET  /deploy/new` / `GET /deploy/{name}/edit` | the builder, empty or loaded |
| `POST /deploy/detect` | classify the omnibox input (repo/registry/image) → a card preview (reuses `sourceInspect` + registry search) |
| `POST /deploy/{draft}/add` | append a card, autosave draft, return the cart partial |
| `POST /deploy/{draft}/card/{i}` | edit/remove/reorder a card |
| `POST /deploy/{draft}/wire` | apply/toggle a wiring suggestion (after/secret/edge) |
| `GET  /deploy/{draft}/recipe` | the assembled TOML (read-only review) |
| `POST /deploy/{draft}/deploy` | marshal → write draft → promote → redirect |
| `GET  /deploy/{name}/raw` / `POST …/raw` | the raw-TOML escape hatch |

Reused as-is: `sourceInspect` (github inspect→prefill), registry search,
`sealAction`, env-file handlers, `deployDelete`, `deployNow`.

**Removed:** the three-lane source UI and its handlers — `sourcePreview`,
`sourceCreate`, `deployStackCreate`, `registryStackForm`, `deployRaw`'s old
form, the `tab=new` machinery, and the whole of `deploy_source.html`. `deploy.html`
is rewritten around the builder; `deployments.html` becomes the clean list.

## Visual language

Keep and *elevate* the terminal aesthetic — it's ply's identity, not a
limitation. Monospace throughout, the orange accent for the one primary action
per screen, muted `edge` separators, health dots. Cards are bordered blocks
(the `ply ui` TUI look), the omnibox is the single bright focal point. Motion is
minimal: a braille/dot spinner on inspect and deploy, a soft height transition
when a card is added. No modal soup — the builder is one page that changes
sections (cart → wire → review) with the cart always visible above.

## Open decisions (recommendations baked in; flag if you disagree)

- **Draft location** `deployments/.drafts/` (recommended) vs server session.
  File wins: survives a dashboard redeploy, on-brand, inspectable.
- **One-card collapse** to a flat order (recommended) vs always a 1-member
  composition. Flat keeps trivial deploys trivial and diff-clean.
- **Raw escape hatch** always present (recommended) — operators must never be
  boxed out of the file.
- **docker:// members** guided-not-blocked now; first-class when the compose
  importer lands (it needs the same "build-from-not-a-repo" member source).

## Task breakdown (bite-sized, TDD, commit per task)

**Phase 1 — model & drafts (no UI).**
1. `cart.go`: the `Card`/`Cart` model + `Cart.ToTOML()` (N cards → composition,
   1 card → flat order). Test: round-trips of 1 and 3 cards; a db card seals.
2. `Cart.FromTOML(spec)`: parse an existing deployment into cards (composition →
   N, flat order → 1). Test: parse the `qa-stack` composition back to 3 cards.
3. Draft store: write/read/promote `.drafts/<name>.toml`; promote = atomic
   rename. Test: promote moves the file and clears the draft.

**Phase 2 — the builder page (happy path).**
4. `GET /deploy/new` renders the empty builder shell (omnibox + empty cart).
5. `POST /deploy/detect`: classify input; repo → inspect card, name → registry
   results, `.img` → image card. Test: each classification.
6. `POST …/add` + `card/{i}`: append/edit/remove/reorder, autosave, return the
   cart partial. Test: add → cart has it; reorder changes `after` default.
7. Review + `POST …/deploy`: assemble → promote → redirect; wire the deploying
   spinner. Test: deploy writes `<name>.toml` and removes the draft.

**Phase 3 — wire.**
8. Wiring suggestions engine: given cards, propose `after` edges + a db
   credential (generate + seal) + a public edge. Test: postgres present →
   suggests after + sealed password + injected `DATABASE_URL`.
9. The wire UI section + toggles; each toggle mutates the draft.

**Phase 4 — edit, raw, list.**
10. `GET /deploy/{name}/edit` loads a deployment into the builder.
11. Raw view/edit escape hatch.
12. Rewrite `deployments.html` into the clean list (kind, status, edit).

**Phase 5 — cutover.**
13. Delete `deploy_source.html` + the dead source-lane handlers; update routes.
14. Prune tests/fixtures for the removed lanes; `go build`/`vet`/`test`/gofmt.
15. Release; verify on the droplet end-to-end (build a fresh 3-service stack via
    the cart, deploy, see it grouped on apps).

## Done when

A first-time user, given a repo URL and "I need a postgres", stands up a wired
web+server+db stack from the dashboard without reading a doc — and an operator
can still drop to the raw file for anything the builder doesn't model.
