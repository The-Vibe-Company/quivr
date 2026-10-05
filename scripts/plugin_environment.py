"""Keep engine signing credentials out of local plugin processes."""
import os


def inherited():
    """Inherit ordinary settings; callers add only this plugin's own key ring."""
    return {key: value for key, value in os.environ.items()
            if key not in {'QUIVR_ENGINE_PLUGIN_KEYS', 'QUIVR_PLUGIN_SIGNING_KEYS'}}
