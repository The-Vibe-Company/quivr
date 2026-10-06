"""Coordinator-deployed L4 embedding service. Importing this file never launches compute.

Local SDK: modal==1.6.1. Deploy from the repository root; see deploy/railway/README.md.
EMBED_MIN_CONTAINERS=0 scales the GPU to zero; set a positive count for warm queries.
"""
import os
import pathlib

import modal

MODEL = 'google/embeddinggemma-2'
REVISION = '914f7f89142e33e77833254d9c9b90c3cef7303b'
ROOT = pathlib.Path(__file__).resolve().parent
MIN_CONTAINERS = int(os.environ.get('EMBED_MIN_CONTAINERS', '0'))
MAX_CONTAINERS = int(os.environ.get('EMBED_MAX_CONTAINERS', '2'))
if not 0 <= MIN_CONTAINERS <= MAX_CONTAINERS or MAX_CONTAINERS < 1:
    raise ValueError('require 0 <= EMBED_MIN_CONTAINERS <= EMBED_MAX_CONTAINERS and a positive maximum')


def cache_model():
    from huggingface_hub import ModelCard, snapshot_download
    card = ModelCard.load(MODEL, revision=REVISION)
    if card.data.license != 'apache-2.0':
        raise ValueError('pinned model card licence must be Apache-2.0')
    snapshot_download(MODEL, revision=REVISION, local_dir='/model',
                      ignore_patterns=['*.bin', '*.h5', '*.msgpack', '*.ot', 'onnx/*'])


app = modal.App('quivr-embeddinggemma')
# Match the proven bake-off stack, including the processor's vision imports.
gpu_image = (modal.Image.debian_slim(python_version='3.12')
             .pip_install('torch==2.8.0', 'torchvision==0.23.0', index_url='https://download.pytorch.org/whl/cu126')
             .pip_install('sentence-transformers==6.1.0', 'transformers==5.19.0', 'pillow==12.3.0')
             .env({'HF_HOME': '/opt/huggingface', 'HF_HUB_DISABLE_TELEMETRY': '1',
                   'TOKENIZERS_PARALLELISM': 'false'})
             .run_function(cache_model)
             .env({'HF_HUB_OFFLINE': '1', 'TRANSFORMERS_OFFLINE': '1'})
             .add_local_file(ROOT / 'embedding_api.py', '/root/embedding_api.py', copy=True))


@app.cls(image=gpu_image, gpu='L4', cpu=4, memory=8192, timeout=90,
         min_containers=MIN_CONTAINERS, max_containers=MAX_CONTAINERS, scaledown_window=60)
class Encoder:
    @modal.enter()
    def load(self):
        from huggingface_hub import ModelCard
        from sentence_transformers import SentenceTransformer
        import torch
        # Recheck the baked model card before loading any weights.
        if ModelCard.load('/model/README.md').data.license != 'apache-2.0':
            raise ValueError('pinned model card licence must be Apache-2.0')
        self.model = SentenceTransformer('/model', device='cuda', local_files_only=True,
            config_kwargs={'vision_config': None, 'audio_config': None},
            model_kwargs={'torch_dtype': torch.bfloat16})

    @modal.batched(max_batch_size=32, wait_ms=20)
    def encode(self, texts: list[str]) -> list[list[float]]:
        from embedding_api import encode_batch
        return encode_batch(self.model, texts)


# Authentication and validation run on CPU before any GPU dispatch. GPU containers
# receive no bearer secret. Both layers scale to zero by default.
web_image = (modal.Image.debian_slim(python_version='3.12')
             .add_local_file(ROOT / 'embedding_api.py', '/root/embedding_api.py', copy=True))


@app.function(image=web_image, secrets=[modal.Secret.from_name('quivr-embeddinggemma', required_keys=['EMBED_API_KEY'])],
              min_containers=0, max_containers=4, scaledown_window=60, timeout=90)
@modal.concurrent(max_inputs=32)
@modal.asgi_app()
def web():
    import asyncio
    from embedding_api import create_app
    encoder = Encoder()

    async def embed(texts):
        # Each call supplies one text; Modal assembles dynamic GPU batches and
        # gather preserves OpenAI's request order even when calls complete out of order.
        return await asyncio.gather(*(encoder.encode.remote.aio(text) for text in texts))

    return create_app(os.environ['EMBED_API_KEY'], embed)
