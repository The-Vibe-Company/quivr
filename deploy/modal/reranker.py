"""Deploy a pinned TEI-compatible reranker on Modal. See deploy/modal/README.md."""
import os
import pathlib

import modal

ROOT = pathlib.Path(__file__).resolve().parent
MODELS = {
    'Qwen/Qwen3-Reranker-4B': ('22e683669bc0f0bd69640a1354a6d0aebcfeede5', 'apache-2.0', 'qwen', 4096),
    'BAAI/bge-reranker-base': ('2cfc18c9415c912f9d8155881c133215df768a70', 'mit', 'classifier', 512),
}
MODEL = os.environ.get('RERANKER_MODEL', 'Qwen/Qwen3-Reranker-4B')
if MODEL not in MODELS:
    raise ValueError('RERANKER_MODEL must select a pinned model supported by this app')
REVISION, LICENSE, MODEL_KIND, MAX_TOKENS = MODELS[MODEL]
MIN_CONTAINERS = int(os.environ.get('RERANKER_MIN_CONTAINERS', '0'))
MAX_CONTAINERS = int(os.environ.get('RERANKER_MAX_CONTAINERS', '2'))
if not 0 <= MIN_CONTAINERS <= MAX_CONTAINERS or MAX_CONTAINERS < 1:
    raise ValueError('require 0 <= RERANKER_MIN_CONTAINERS <= RERANKER_MAX_CONTAINERS and a positive maximum')
BATCH_SIZE = 8


def cache_model(model, revision, license_id):
    from huggingface_hub import ModelCard, hf_hub_download, snapshot_download

    card = ModelCard.load(hf_hub_download(model, 'README.md', revision=revision))
    if card.data.license != license_id:
        raise ValueError('pinned model card license does not match the expected license')
    snapshot_download(model, revision=revision, local_dir='/model',
                      allow_patterns=['README.md', '*.json', '*.txt', '*.model', '*.safetensors'])


app = modal.App('quivr-reranker')
gpu_image = (modal.Image.debian_slim(python_version='3.12')
             .pip_install('torch==2.8.0', index_url='https://download.pytorch.org/whl/cu126')
             .pip_install('transformers==5.19.0', 'huggingface-hub==2.5.0', 'sentencepiece==0.2.1')
             .env({'HF_HOME': '/opt/huggingface', 'HF_HUB_DISABLE_TELEMETRY': '1',
                   'TOKENIZERS_PARALLELISM': 'false'})
             .run_function(cache_model, args=(MODEL, REVISION, LICENSE))
             .env({'HF_HUB_OFFLINE': '1', 'TRANSFORMERS_OFFLINE': '1', 'RERANKER_MODEL': MODEL})
             .add_local_file(ROOT / 'reranker_api.py', '/root/reranker_api.py', copy=True))


@app.cls(image=gpu_image, gpu=['L4', 'A10G', 'L40S'], cpu=4, memory=8192, timeout=90,
         min_containers=MIN_CONTAINERS, max_containers=MAX_CONTAINERS, scaledown_window=60)
class Reranker:
    @modal.enter()
    def load(self):
        import torch
        from huggingface_hub import ModelCard
        from transformers import AutoModelForCausalLM, AutoModelForSequenceClassification, AutoTokenizer
        from reranker_api import QWEN_PREFIX, QWEN_SUFFIX

        if ModelCard.load('/model/README.md').data.license != LICENSE:
            raise ValueError('baked model card license does not match the expected license')
        self.tokenizer = AutoTokenizer.from_pretrained('/model', local_files_only=True, trust_remote_code=False)
        model_class = AutoModelForSequenceClassification
        if MODEL_KIND == 'qwen':
            model_class = AutoModelForCausalLM
            self.tokenizer.padding_side = 'left'
            self.prefix = self.tokenizer.encode(QWEN_PREFIX, add_special_tokens=False)
            self.suffix = self.tokenizer.encode(QWEN_SUFFIX, add_special_tokens=False)
            self.label_ids = [self.tokenizer.convert_tokens_to_ids(label) for label in ('no', 'yes')]
        self.model = model_class.from_pretrained(
            '/model', local_files_only=True, trust_remote_code=False, use_safetensors=True,
            dtype=torch.bfloat16,
        ).to('cuda').eval()

    def prepare(self, query, texts, instruction, truncate, direction):
        from reranker_api import classifier_query, qwen_pair

        self.tokenizer.truncation_side = direction
        if MODEL_KIND == 'qwen':
            pairs = [qwen_pair(query, text, instruction) for text in texts]
            budget = MAX_TOKENS - len(self.prefix) - len(self.suffix)
            bodies = self.tokenizer(pairs, add_special_tokens=False, truncation=truncate,
                                    max_length=budget, verbose=False)['input_ids']
            return [{'input_ids': self.prefix + body + self.suffix} for body in bodies]
        queries = [classifier_query(query, instruction)] * len(texts)
        features = self.tokenizer(queries, texts, truncation=truncate, max_length=MAX_TOKENS,
                                  verbose=False)
        return [dict(zip(features, row)) for row in zip(*features.values())]

    @modal.method()
    def score(self, query, texts, instruction, truncate, direction):
        import torch
        from reranker_api import logit_margin

        inputs = self.prepare(query, texts, instruction, truncate, direction)
        if any(len(pair['input_ids']) > MAX_TOKENS for pair in inputs):
            return None
        margins = []
        with torch.inference_mode():
            for start in range(0, len(inputs), BATCH_SIZE):
                batch = self.tokenizer.pad(inputs[start:start + BATCH_SIZE], padding=True,
                                           return_tensors='pt').to('cuda')
                if MODEL_KIND == 'qwen':
                    logits = self.model(**batch, use_cache=False, logits_to_keep=1).logits[:, -1, self.label_ids]
                else:
                    logits = self.model(**batch).logits
                margins.extend(logit_margin(row) for row in logits.float().tolist())
        return margins


# The bearer key exists only on the CPU gateway.
web_image = (modal.Image.debian_slim(python_version='3.12')
             .env({'RERANKER_MODEL': MODEL})
             .add_local_file(ROOT / 'reranker_api.py', '/root/reranker_api.py', copy=True))


@app.function(image=web_image, secrets=[modal.Secret.from_name('quivr-reranker', required_keys=['RERANKER_API_KEY'])],
              min_containers=0, max_containers=4, scaledown_window=60, timeout=90)
@modal.concurrent(max_inputs=16)
@modal.asgi_app()
def web():
    from reranker_api import create_app

    reranker = Reranker()

    async def score(request):
        return await reranker.score.remote.aio(request.query, request.texts, request.instruction,
                                               request.truncate, request.truncation_direction)

    return create_app(os.environ['RERANKER_API_KEY'], score, model=MODEL, revision=REVISION)
