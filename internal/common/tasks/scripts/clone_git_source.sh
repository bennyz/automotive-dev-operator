#!/bin/sh
set -eu

git_home=$(mktemp -d)
trap 'rm -rf "$git_home"' EXIT
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_TERMINAL_PROMPT=0
if [ -s /source-ca/ca-bundle.crt ]; then
  system_ca=""
  for candidate in /etc/ssl/certs/ca-certificates.crt /etc/pki/tls/certs/ca-bundle.crt /etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem; do
    if [ -s "$candidate" ]; then system_ca="$candidate"; break; fi
  done
  if [ -n "$system_ca" ]; then
    cat "$system_ca" /source-ca/ca-bundle.crt > "$git_home/ca-bundle.crt"
  else
    cat /source-ca/ca-bundle.crt > "$git_home/ca-bundle.crt"
  fi
  export GIT_SSL_CAINFO="$git_home/ca-bundle.crt"
fi
if [ -d /git-auth ]; then
  cat > "$git_home/askpass" <<'EOF'
#!/bin/sh
case "$1" in
  *Username*) cat /git-auth/username ;;
  *Password*) cat /git-auth/password ;;
  *) exit 1 ;;
esac
EOF
  chmod 700 "$git_home/askpass"
  export GIT_ASKPASS="$git_home/askpass"
fi

mkdir -p "$SOURCE_WORKSPACE/.caib-source"
cd "$SOURCE_WORKSPACE/.caib-source"
rm -rf repository
git init -q repository
cd repository
git remote add origin "$SOURCE_URL"
git -c http.followRedirects=false -c protocol.file.allow=never fetch --quiet --depth=1 --no-tags origin "${SOURCE_REVISION:-HEAD}"
git -c advice.detachedHead=false checkout --quiet --detach FETCH_HEAD
git rev-parse HEAD > ../commit
if git ls-files --stage | grep -q '^160000 '; then
  echo "Git submodules are not supported for image sources" >&2
  exit 1
fi
rm -rf .git
