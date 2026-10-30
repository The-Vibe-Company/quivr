FROM python:3.12-slim-bookworm@sha256:e697c81897302a4c4333bade000d98038fbb21eb064f8ee1fd42d02ebcd9d337 AS model
WORKDIR /app
COPY third_party/e5/model-lock.json ./third_party/e5/model-lock.json
COPY scripts/prepare_embeddings.py ./scripts/prepare_embeddings.py
RUN python scripts/prepare_embeddings.py
RUN python -c "import json; lock=json.load(open('third_party/e5/model-lock.json')); open('/e5.sha256','w').write(''.join(sha+'  /models/e5/'+name+'\\n' for name,sha in lock['files'].items()))"

FROM ghcr.io/huggingface/text-embeddings-inference@sha256:ad950d30878eceb72aaf32024d26fa2b1d04a75304fa0b4776b49aa1941fea07
# Keep the real TEI CPU runtime; repair its fixed critical Debian packages.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      libssl-dev=3.0.22-1~deb12u2 libssl3=3.0.22-1~deb12u2 openssl=3.0.22-1~deb12u2 \
      libgnutls30=3.7.9-2+deb12u7 libssh2-1=1.10.0-3+deb12u1 perl-base=5.36.0-7+deb12u4 libbrotli1=1.0.9-2+deb12u1 \
 && rm -rf /var/lib/apt/lists/*
COPY --from=model /app/.scratch/e5-model /model
COPY --from=model /e5.sha256 /app/e5.sha256
COPY deploy/single-machine/tei-entrypoint.sh /app/tei-entrypoint.sh
ENV HF_HUB_OFFLINE=1
CMD ["--model-id", "/model", "--dtype", "float32", "--pooling", "mean", "--max-client-batch-size", "32", "--max-batch-tokens", "8192", "--auto-truncate", "false", "--tokenization-workers", "2"]
