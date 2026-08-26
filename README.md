# ply-dashboard

The opt-in web UI for [ply](https://plybox.sh) — **and it is just a ply app.**

No daemon, no database, no agent. A single static Go binary (htmx + Tailwind,
everything embedded) that reads the same files the `ply` CLI reads, through
bind mounts you grant explicitly. ply is 100% functional without it;
uninstalling is `ply rm dashboard`.

![dark, dense, terminal-spirit UI]

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/iluxav/ply-dashboard/main/install.sh | sh
```

or from the ply registry:

```sh
ply run dashboard@0.1 --grant-links --publish internal:7070
```

Add `--domain ply.example.com` and the [ply edge](https://plybox.sh/docs/running/)
serves it over HTTPS. The image *requests* its four read surfaces in
`[requests] links` (ply's run dir, apps dir, cgroups, host /proc) and
**`--grant-links` is the operator's explicit yes** — without it ply lists
the requests and mounts nothing. The grant is visible in `ply ps`,
auditable in the systemd unit, revocable by restarting without the flag.

## First boot

A **setup token** is printed to the log (`journalctl -u ply-dashboard` or the
`ply run` output). The create-account page requires it — so whoever reads the
host's logs owns the dashboard, and nobody else does. Credentials are an
argon2id hash in the `data` volume; sessions are signed cookies. Forgot the
password? Delete `auth.json` from the volume — the filesystem is the admin
API.

## What it shows (v1)

- every app: health, instances, restarts, uptime, version, published
  address, domains
- per-instance CPU/memory sparklines (cgroup v2, `/proc` fallback rootless)
- a command panel: every action as a copy-paste `ply` command

v1 observes; the terminal acts. Buttons arrive with ply's control-dir
protocol (commands as files — the daemontools way), at which point the
dashboard gains exactly the powers you grant by mounting the control dirs
read-write, and not one more.

## Dev

```sh
make run        # builds CSS + binary, serves :7070 against your local ply state
make test
make img        # the ply image (needs ply installed)
```

Tailwind's standalone binary lands in `bin/` (gitignored):

```sh
curl -fsSL -o bin/tailwindcss https://github.com/tailwindlabs/tailwindcss/releases/download/v3.4.17/tailwindcss-linux-x64 && chmod +x bin/tailwindcss
```
