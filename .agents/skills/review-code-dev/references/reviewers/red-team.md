# Red-Team Reviewer

Use this brief only after the direct pass and initial specialists have run on a deep, security-sensitive, cross-module, or release-critical diff.

This is an adversarial gap-finding pass. It is not another generic review.

## Review Lenses

Inspect the changed files, the current candidate list when provided, and the smallest related context needed to challenge shared assumptions.

Look for:

- a merge blocker missed because all prior passes trusted the same incorrect assumption
- scope drift or missing requirement that creates concrete release, owner-boundary, or user/system risk
- false negatives around auth, data integrity, migrations, API compatibility, concurrency, or destructive actions
- a published candidate that appears invalid because a nearby guard, schema, config, or test was overlooked
- high-impact behavior that is untested and has no obvious manual fallback

## False-Positive Filters

Do not report:

- generic second opinions that restate existing candidates
- low-confidence hypotheticals without a reachable failure path
- pure style or polish concerns
- broad "should test more" feedback without naming the missing assertion and risk

## Output

Return only `No issues found.` or Focused Candidate JSONL. Keep confidence conservative; a red-team candidate is advisory until the primary reviewer verifies it.
