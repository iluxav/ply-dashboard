#!/bin/sh
# ply-dashboard installer — downloads the right-arch image from the latest
# GitHub release and prints the run command. Needs ply.
set -eu

command -v ply >/dev/null || {
  echo "ply is required first: curl -fsSL https://plybox.sh/install.sh | sh" >&2; exit 1; }

case "$(uname -m)" in
  x86_64) ARCH=x64 ;;
  aarch64) ARCH=arm64 ;;
  *) echo "unsupported arch $(uname -m)" >&2; exit 1 ;;
esac

REPO=iluxav/ply-dashboard
TAG=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')
[ -n "$TAG" ] || { echo "cannot determine the latest release" >&2; exit 1; }
VER=${TAG#v}
IMG="dashboard-$VER-linux-$ARCH.img"

echo "downloading $IMG ($TAG)…"
curl -fsSL -o "$IMG" "https://github.com/$REPO/releases/download/$TAG/$IMG"

cat <<DONE

$IMG downloaded. Run it:

  ply run $IMG --grant-links --publish internal:7070

(--grant-links mounts the read surfaces the image requests: ply's run dir,
apps dir, cgroups, host /proc. Needs ply >= 0.1.22.)
Add --domain ply.example.com for HTTPS via the ply edge (sudo ply setup --edge).
First boot prints a setup token to the log — you need it to create the account.
DONE
