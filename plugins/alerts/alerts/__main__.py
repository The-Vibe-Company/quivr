"""Serve the plugin: python3 -m alerts (quivr plugin dev runs this)."""
import logging

from .rule import classifier, plugin

logging.getLogger("alerts").warning(
    "described alerts are %s", "enabled (TYPESAFE_API_KEY is set)" if classifier() else "disabled: TYPESAFE_API_KEY is not set")
plugin.serve()
