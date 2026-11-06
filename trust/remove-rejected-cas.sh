#!/bin/sh
set -eu
policy=; bundles=; roots=; report=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --policy|--fingerprints) policy=$2; shift 2 ;;
    --bundle) bundles="$bundles
$2"; shift 2 ;;
    --scan-root) roots="$roots
$2"; shift 2 ;;
    --report) report=$2; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
[ -r "$policy" ] || { echo "trust policy missing: $policy" >&2; exit 1; }
[ -n "$bundles" ] || bundles="/etc/ssl/certs/ca-certificates.crt"
[ -n "$roots" ] || roots="/usr/share/ca-certificates
/usr/local/share/ca-certificates
/etc/ssl/certs
/etc/ca-certificates/extracted"
for tool in awk find openssl sha256sum readlink stat cp mv chmod cat mkdir; do command -v "$tool" >/dev/null 2>&1 || { echo "$tool is required before trust qualification" >&2; exit 127; }; done
entries=$(awk -v expected='7dd78d5f4f13459a83dff9abbba62edbaf6f2d102bf257fd712f4d9f2746ed8d,d7a7a0fb5d7e2731d771e9484ebcdef71d5f0c3e0a2948782bc83ee0ea699ef4,86a1ecba089c4a8d3bbe2734c612ba341d813e043cf9e8a862cd5c57a36bbe6b' '{ line=$0; sub(/^[[:space:]]*/, "", line); if (line=="" || substr(line,1,1)=="#") next; split(line,f,/[\t ]+/); if (length(f)!=1 || length(f[1])!=64 || f[1] !~ /^[[:xdigit:]]+$/) bad=1; if (seen[tolower(f[1])]++) duplicate=1; valid++ } END { count=split(expected, wanted, ","); if (bad || duplicate || valid != count) exit 1; for (i=1; i<=count; i++) if (!seen[wanted[i]]) exit 1; print valid }' "$policy") || { echo "invalid or non-reviewed trust policy" >&2; exit 1; }
work=${TRUST_WORK_DIR:-/tmp/quix-trust-work}.$$
mkdir -p "$work"; trap 'rm -rf "$work"' EXIT HUP INT TERM
real() { readlink -f "$1" 2>/dev/null || printf '%s\n' "$1"; }
rejected() { awk -v wanted="$1" '{ line=$0; sub(/^[[:space:]]*/, "", line); if (line=="" || substr(line,1,1)=="#") next; split(line,f,/[\t ]+/); if (tolower(f[1])==tolower(wanted)) found=1 } END { exit(found ? 0 : 1) }' "$policy"; }
required() { target=$1; old=$IFS; IFS=$(printf '\n_'); IFS=${IFS%_}; for b in $bundles; do [ -n "$b" ] && [ "$(real "$b")" = "$target" ] && { IFS=$old; return 0; }; done; IFS=$old; return 1; }
digest() { openssl x509 -in "$1" -outform DER -out "$2" >/dev/null 2>&1 && test -s "$2" && sha256sum "$2" | awk '{print tolower($1)}'; }
split_pem() { awk -v d="$2" '/-----BEGIN CERTIFICATE-----/{n++; if (inside) exit 2; p=d"/cert-"n".pem";inside=1} inside{print > p} /-----END CERTIFICATE-----/{if (!inside) exit 3; closes++; close(p);inside=0} END{if (inside || n != closes) exit 4; if (!n) exit 5}' "$1"; }
sanitize() {
  input=$1; target=$(real "$input"); [ -f "$target" ] || return 0
  case "$input" in
    *.der)
      der="$work/$(printf '%s' "$target" | sha256sum | awk '{print $1}').der"
      sum=$(openssl x509 -inform DER -in "$target" -outform DER -out "$der" >/dev/null 2>&1 && sha256sum "$der" | awk '{print tolower($1)}') || { echo "invalid DER certificate: $input" >&2; exit 1; }
      if rejected "$sum"; then
        if required "$target"; then echo "required trust bundle would become empty: $input" >&2; exit 1; fi
        if [ -L "$input" ]; then rm -f "$input"; else rm -f "$target"; fi
        printf '%s %s %s\n' "$input" 0 1 >> "$work/report"
      fi
      return 0
      ;;
  esac
  d="$work/$(printf '%s' "$target" | sha256sum | awk '{print $1}')"; rm -rf "$d"; mkdir -p "$d"
  if split_pem "$target" "$d"; then :; else status=$?; if [ "$status" -eq 5 ]; then required "$target" && { echo "required trust bundle is empty or unparsable: $input" >&2; exit 1; }; return 0; fi; echo "malformed certificate bundle: $input" >&2; exit 1; fi
  out="$d/filtered.pem"; : > "$out"; found=0; kept=0; removed=0
  for cert in "$d"/cert-*.pem; do [ -f "$cert" ] || continue; found=$((found+1)); sum=$(digest "$cert" "$cert.der") || { echo "invalid X.509 certificate: $input" >&2; exit 1; }; if rejected "$sum"; then removed=$((removed+1)); else cat "$cert" >> "$out"; kept=$((kept+1)); fi; done
  [ "$found" -gt 0 ] || return 0
  if [ "$kept" -gt 0 ]; then
    if [ "$removed" -gt 0 ]; then mode=$(stat -c '%a' "$target" 2>/dev/null || stat -f '%Lp' "$target"); tmp="$target.quix-trust.$$"; cp "$out" "$tmp"; chmod "$mode" "$tmp"; mv -f "$tmp" "$target"; fi
  elif required "$target"; then echo "required trust bundle would become empty: $input" >&2; exit 1
  elif [ -L "$input" ]; then rm -f "$input"
  else rm -f "$target"
  fi
  printf '%s %s %s\n' "$input" "$kept" "$removed" >> "$work/report"
}
old=$IFS; IFS=$(printf '\n_'); IFS=${IFS%_}
for b in $bundles; do [ -n "$b" ] || continue; [ -r "$(real "$b")" ] || { echo "required trust bundle missing: $b" >&2; exit 1; }; done
for root in $roots; do
  [ -d "$root" ] || continue
  find "$root" \( -type f -o -type l \) -print > "$work/paths"
  while IFS= read -r path; do
    case "$path" in *.crt|*.pem|*.cer|*.der|*.0|*.1|*.2|*.3|*.4|*.5|*.6|*.7|*.8|*.9) target=$(real "$path"); required "$target" || sanitize "$path" ;; esac
  done < "$work/paths"
done
IFS=$old
if command -v update-ca-certificates >/dev/null 2>&1; then update-ca-certificates --fresh >/dev/null; fi
old=$IFS; IFS=$(printf '\n_'); IFS=${IFS%_}; for b in $bundles; do [ -n "$b" ] && sanitize "$b"; done; IFS=$old
if [ -n "$report" ]; then mkdir -p "$(dirname "$report")"; { printf '{"policy_entries":%s,"files":[' "$entries"; first=1; while IFS=' ' read -r path kept removed; do [ "$first" -eq 1 ] || printf ','; first=0; printf '{"path":"%s","kept":%s,"removed":%s}' "$path" "$kept" "$removed"; done < "$work/report" 2>/dev/null || true; printf '%s\n' ']}'; } > "$report"; fi
