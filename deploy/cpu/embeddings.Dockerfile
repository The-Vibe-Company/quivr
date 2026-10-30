# The digest is the linux/amd64 OCI index returned by the Docker Hub registry
# for python:3.12-slim-bookworm on 2026-10-30.
FROM python:3.12-slim-bookworm@sha256:e697c81897302a4c4333bade000d98038fbb21eb064f8ee1fd42d02ebcd9d337

WORKDIR /app

# The runtime owns both offline model preparers. The cache wrapper selects one
# model and writes installation-model.json only after its files verify.
COPY third_party/query-encoder/requirements.txt /app/third_party/query-encoder/requirements.txt
RUN python -m venv /opt/query-encoder/venv \
 && /opt/query-encoder/venv/bin/pip install --no-cache-dir --disable-pip-version-check \
      --require-hashes -r /app/third_party/query-encoder/requirements.txt

COPY third_party/query-encoder/model-lock.json /app/third_party/query-encoder/model-lock.json
COPY third_party/e5/model-lock.json /app/third_party/e5/model-lock.json
COPY third_party/query-encoder/NOTICE.md third_party/query-encoder/LICENSE.apache-2.0 /usr/share/quivr/notices/embeddinggemma/
COPY scripts/prepare_query_encoder.py /app/scripts/prepare_query_encoder.py
COPY scripts/prepare_embeddings.py /app/scripts/prepare_embeddings.py
COPY deploy/cpu/text_encoder.py /app/deploy/cpu/text_encoder.py
COPY deploy/cpu/embedding_server.py /app/deploy/cpu/embedding_server.py
COPY deploy/modal/embedding_api.py /app/deploy/modal/embedding_api.py
COPY deploy/single-machine/cache.py /app/deploy/single-machine/cache.py

RUN mkdir -p /models/gemma /models/e5 \
 && chown -R 10001:10001 /models

ENV PATH=/opt/query-encoder/venv/bin:$PATH \
    PYTHONPATH=/app \
    HF_HUB_OFFLINE=1 \
    TRANSFORMERS_OFFLINE=1 \
    QUIVR_EMBEDDING_HOST=0.0.0.0 \
    QUIVR_EMBEDDING_PORT=8080 \
    QUIVR_MODEL_DIR=/models/gemma \
    QUIVR_EMBEDDING_THREADS=4

USER 10001:10001
ENTRYPOINT ["/opt/query-encoder/venv/bin/python", "-m", "deploy.cpu.embedding_server"]
CMD ["serve"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
  CMD ["/opt/query-encoder/venv/bin/python", "-m", "deploy.cpu.embedding_server", "healthcheck"]
