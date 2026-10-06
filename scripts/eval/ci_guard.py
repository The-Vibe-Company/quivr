"""Shared environment policy for evaluation commands that must stay out of CI."""
import os


def in_ci():
    return any(os.environ.get(name, '').lower() not in ('', '0', 'false')
               for name in ('CI', 'GITHUB_ACTIONS'))
