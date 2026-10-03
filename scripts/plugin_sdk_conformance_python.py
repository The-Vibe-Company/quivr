"""Offline Python peer for normative push and ingestion certification."""
import base64
import hashlib
import hmac
import json
import urllib.request

from quivr_plugin import Plugin

plugin = Plugin()
ATTACHMENT = b"Offline attachment bytes."


class AttachmentMethods:
    def describe_attachment(self, request):
        return {"size_bytes": len(ATTACHMENT), "sha256": hashlib.sha256(ATTACHMENT).hexdigest()}

    def upload_attachment(self, request):
        grant = request.grant
        upload = urllib.request.Request(grant.url, data=ATTACHMENT, method=grant.method, headers=grant.headers)
        with urllib.request.urlopen(upload, timeout=5) as response:
            response.read()
        return {"status": "uploaded"}



@plugin.segment_and_embed
def segment(request):
    spaces = plugin.manifest.model.contributions.ingestion.spaces
    return {"segments": [{"part_key": p.key, "start": 0, "end": len(p.text),
                          "vectors": {s: [1.0] + [0.0] * (spaces[s].dimensions - 1) for s in request.spaces}}
                         for p in request.parts]}


@plugin.embed_query
def query(request):
    dimension = plugin.manifest.model.contributions.ingestion.spaces[request.space].dimensions
    return {"vector": [1.0] + [0.0] * (dimension - 1)}


@plugin.connector("alerts")
class Alerts(AttachmentMethods):
    def fetch(self, request):
        return {"items": [] if request.checkpoint else [{"record_key": "alert-6", "content": {"kind": "text", "text": "A new alert."}}],
                "checkpoint": {"done": True}, "more": False}

    def check_credential(self, request):
        return {"status": "ok"}

    def receive(self, request):
        def answer(verdict, status, body="", items=None):
            return {"verdict": verdict, "response": {"status": status, "body": body}, "items": items or []}
        if request.request.method == "GET":
            return answer("accepted", 200, "response_token")
        body = base64.b64decode(request.request.body_base64)
        key = request.credential["signing_secret"].encode()
        expected = "sha256=" + base64.b64encode(hmac.new(key, body, hashlib.sha256).digest()).decode()
        actual = request.request.headers.get("x-example-signature", [""])[0]
        if not hmac.compare_digest(actual, expected):
            return answer("refused", 401, "Invalid signature")
        document = json.loads(body)
        return answer("accepted", 200, items=[{"record_key": a["id"], "content": {"kind": "text", "text": a["text"]}}
                                               for a in document["alerts"]])


@plugin.connector("files")
class Files(AttachmentMethods):
    def fetch(self, request):
        items = [] if request.checkpoint else [{
            "record_key": "file-1", "content": {"kind": "manifest", "parts": [{"key": "body", "role": "body", "content": {"kind": "text", "text": "An attachment."}}]},
            "attachments": [{"key": "file", "role": "attachment", "media_type": "application/octet-stream", "ref": "offline-file"}],
        }]
        return {"items": items, "checkpoint": {"done": True}, "more": False}

    def check_credential(self, request):
        return {"status": "ok"}


if __name__ == "__main__":
    plugin.serve()
