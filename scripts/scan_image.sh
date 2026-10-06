#!/usr/bin/env bash
# One vulnerability policy for local PR images and immutable release images.
set -euo pipefail
image=${1:?usage: scan_image.sh docker:image|registry:image}
export GRYPE_CHECK_FOR_APP_UPDATE=false
grype "$image" --only-fixed --fail-on critical --output json > vulnerabilities.json
