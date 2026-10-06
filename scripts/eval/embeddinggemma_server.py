"""Ephemeral loopback text-only EmbeddingGemma 2 adapter for the Modal bake-off.

Serving follows the pinned model card, including its licence, encoder selection
and precision rules: https://huggingface.co/google/embeddinggemma-2/blob/
914f7f89142e33e77833254d9c9b90c3cef7303b/README.md
Dependencies and weights are loaded only when the coordinator starts the server.
"""
import http.server
import json
import sys

MODEL = 'google/embeddinggemma-2'
REVISION = '914f7f89142e33e77833254d9c9b90c3cef7303b'
LABELS = {'embeddinggemma-2': 768, 'embeddinggemma-2-256': 256}


def load_model(hardware):
    from huggingface_hub import ModelCard, hf_hub_download
    # Fail closed before loading any weights. Never substitute the gated v1.
    # ModelCard.load takes no revision: read the pinned README itself.
    card = ModelCard.load(hf_hub_download(MODEL, 'README.md', revision=REVISION))
    if card.data.license not in ('apache-2.0', 'mit'):
        raise ValueError('model card licence must be Apache-2.0 or MIT')
    import torch
    from sentence_transformers import SentenceTransformer
    return SentenceTransformer(MODEL, revision=REVISION,
        device='cpu' if hardware == 'cpu' else 'cuda',
        config_kwargs={'vision_config': None, 'audio_config': None},
        model_kwargs={'torch_dtype': torch.float32 if hardware == 'cpu' else torch.bfloat16})


def encode_request(model, body):
    """Prompts arrive from the benchmark client; suppress ST's default prompt."""
    dimension = body.get('dimensions', 768)
    texts = body.get('input')
    if (body.get('model') != MODEL or type(dimension) is not int or dimension not in LABELS.values()
            or not isinstance(texts, list) or not 1 <= len(texts) <= 32
            or any(not isinstance(text, str) for text in texts)):
        raise ValueError('invalid embedding request')
    from direct_bakeoff import normalize
    try:
        vectors = model.encode(texts, prompt='', batch_size=16, show_progress_bar=False,
                               convert_to_numpy=True)
        if vectors.shape != (len(texts), 768):
            raise RuntimeError('unexpected native embedding shape')
        # Normalize after slicing, even though the full model output is normalized.
        vectors = normalize(vectors[:, :dimension])
    except (ValueError, TypeError):
        raise RuntimeError('encoding failed') from None
    return {'data': [{'index': i, 'embedding': vector.tolist()} for i, vector in enumerate(vectors)]}


def serve(hardware):
    model = load_model(hardware)

    class Handler(http.server.BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass  # No input or diagnostic payloads in progress logs.

        def reply(self, status, body):
            raw = json.dumps(body, allow_nan=False).encode()
            self.send_response(status)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

        def do_GET(self):
            self.reply(200 if self.path == '/health' else 404, {})

        def do_POST(self):
            if self.path != '/v1/embeddings':
                self.reply(404, {})
                return
            try:
                size = int(self.headers.get('Content-Length', '0'))
                if not 0 < size <= 4 * 1024 * 1024:
                    raise ValueError('request size')
                body = json.loads(self.rfile.read(size))
                if not isinstance(body, dict):
                    raise ValueError('request object')
                self.reply(200, encode_request(model, body))
            except (ValueError, TypeError):
                self.reply(400, {'error': 'invalid embedding request'})
            except Exception:
                self.reply(500, {'error': 'encoding failed'})

    with http.server.HTTPServer(('127.0.0.1', 8080), Handler) as server:
        server.serve_forever()


if __name__ == '__main__':
    if len(sys.argv) != 2 or sys.argv[1] not in ('cpu', 'L4'):
        raise SystemExit('expected hardware cpu or L4')
    serve(sys.argv[1])
