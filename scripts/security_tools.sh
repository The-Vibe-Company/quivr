#!/usr/bin/env bash
# Install only checksum-pinned Linux amd64 releases; never run upstream installers.
set -euo pipefail
tool=${1:?usage: security_tools.sh syft|grype destination}
destination=${2:?missing destination}
case "$tool" in
  syft)
    version=1.54.0
    checksum=54a87372498168b2d033e876fd41fa4e8035b872699e525a57046e1f2f09c860
    ;;
  grype)
    version=0.120.0
    checksum=a5a1218dce63acdac152a6b3b5bb366e7267e36f4069848cf455543b3fa5700e
    ;;
  *) echo "unsupported security tool: $tool" >&2; exit 2 ;;
esac
archive=$(mktemp)
trap 'rm -f "$archive"' EXIT
curl --fail --location --silent --show-error --retry 3 --max-time 180 \
  "https://github.com/anchore/$tool/releases/download/v$version/${tool}_${version}_linux_amd64.tar.gz" \
  --output "$archive"
if ! printf '%s  %s\n' "$checksum" "$archive" | sha256sum --check --status; then
  echo "checksum verification failed for ${tool}_${version}_linux_amd64.tar.gz" >&2
  exit 1
fi
mkdir -p "$destination"
tar -xzf "$archive" -C "$destination" "$tool"
"$destination/$tool" version
