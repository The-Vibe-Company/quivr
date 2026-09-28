"""Quivr Plugin SDK for Python: write Plugin Protocol v0 plugins.

See sdks/python/README.md and contracts/plugins/v0/README.md.
"""
import logging as _logging

from .blob import read_input
from .errors import ConfigurationError, PluginError, RetryableError, TerminalError
from .logs import configure_logging, current_invocation_id, invocation_context
from .manifest import PLUGIN_API_VERSION, LoadedManifest, ManifestError, load_manifest, validate_configuration
from .models import *  # noqa: F403
from .models import __all__ as _models
from .server import Invocation, Plugin, Reply

__version__ = "0.1.0"

# Library logging stays silent unless the plugin configures handlers (Plugin.serve does).
_logging.getLogger("quivr_plugin").addHandler(_logging.NullHandler())

__all__ = [
    *_models,
    "ConfigurationError",
    "Invocation",
    "LoadedManifest",
    "ManifestError",
    "PLUGIN_API_VERSION",
    "Plugin",
    "PluginError",
    "Reply",
    "RetryableError",
    "TerminalError",
    "configure_logging",
    "current_invocation_id",
    "invocation_context",
    "load_manifest",
    "read_input",
    "validate_configuration",
]
