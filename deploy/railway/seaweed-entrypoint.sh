#!/bin/sh
set -eu
# Provisioning generates hexadecimal credentials; reject values unsafe for JSON.
case "${S3_ACCESS_KEY:?}${S3_SECRET_KEY:?}" in *[!a-f0-9]*) echo 'Expected hexadecimal S3 credentials' >&2; exit 1;; esac
umask 077
printf '{"identities":[{"name":"demo-core","credentials":[{"accessKey":"%s","secretKey":"%s"}],"actions":["Admin","Read","Write","List","Tagging"]}]}' "$S3_ACCESS_KEY" "$S3_SECRET_KEY" > /tmp/s3.json
exec weed mini -dir=/data -s3.config=/tmp/s3.json
