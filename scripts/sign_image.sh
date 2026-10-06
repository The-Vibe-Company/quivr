#!/usr/bin/env bash
# Release and local PR checks share the complete sign/attest/verify path.
set -euo pipefail
if [[ $# != 2 && $# != 4 ]]; then
  echo 'usage: sign_image.sh image@sha256:digest sbom.spdx.json [private.key public.key]' >&2
  exit 2
fi
image=$1
sbom=$2
signing=()
verification=()
if [[ $# == 4 ]]; then
  signing=(--key "$3")
  verification=(--key "$4")
else
  verification=(
    --certificate-oidc-issuer https://token.actions.githubusercontent.com
    --certificate-identity "${COSIGN_CERTIFICATE_IDENTITY:?missing release signing identity}"
  )
fi
output=$(mktemp -d)
trap 'rm -rf "$output"' EXIT

bounded() {
  local operation=$1 started=$SECONDS status
  shift
  echo "Starting cosign $operation (60s context deadline; 65s hard timeout; 5s kill grace)"
  # Cosign's context deadline does not cover blocked stdout or every dependency.
  # Never send the multi-megabyte, single-line DSSE payload to the Actions runner.
  if timeout --kill-after=5s 65s cosign "$@" --timeout 60s > "$output/$operation.json"; then
    echo "cosign $operation completed successfully in $((SECONDS - started))s"
  else
    status=$?
    echo "::error::cosign $operation failed (exit $status, $((SECONDS - started))s; context 60s, hard timeout 65s, kill grace 5s)" >&2
    exit "$status"
  fi
}

bounded sign sign --yes "${signing[@]}" "$image"
bounded verify verify "${verification[@]}" "$image"
bounded attest attest --yes "${signing[@]}" --type spdxjson --predicate "$sbom" "$image"
bounded verify-attestation verify-attestation "${verification[@]}" --type spdxjson "$image"
