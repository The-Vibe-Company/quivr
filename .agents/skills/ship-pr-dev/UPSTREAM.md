# Attribution

ship-pr-dev is distributed under the MIT License (see LICENSE).
Copyright (c) 2026 The Vibe Company.
Imported package version: 1.6.0.
The imported workflow, references, metadata and evals are unchanged.

Armada adaptations after independent review:
- collect_ship_context.py fails clearly when no base resolves, checks Git command
  failures instead of emitting incomplete branch context, and inventories CI
  directory contents (including .circleci/config.yml).
- prepare_ship_run.py refuses symlinked artifact directory components before
  writing, keeping artifacts inside the checkout.
- The collector regression tests are extended and test_prepare_ship_run.py is
  added for these failures. Modified helpers carry change notices.
