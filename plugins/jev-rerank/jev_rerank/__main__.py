from quivr_plugin import Plugin

from .retriever import Retriever

plugin = Plugin()
plugin.retrieval(Retriever().search)
plugin.serve()
