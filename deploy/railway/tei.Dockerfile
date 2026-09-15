FROM python:3.12-slim-bookworm AS model
WORKDIR /app
COPY third_party/e5/model-lock.json ./third_party/e5/model-lock.json
COPY scripts/prepare_embeddings.py ./scripts/prepare_embeddings.py
RUN python scripts/prepare_embeddings.py

FROM ghcr.io/huggingface/text-embeddings-inference@sha256:ad950d30878eceb72aaf32024d26fa2b1d04a75304fa0b4776b49aa1941fea07
COPY --from=model /app/.scratch/e5-model /model
ENV HF_HUB_OFFLINE=1
CMD ["--model-id", "/model", "--dtype", "float32", "--pooling", "mean", "--max-client-batch-size", "32", "--max-batch-tokens", "8192", "--auto-truncate", "false", "--tokenization-workers", "2"]
