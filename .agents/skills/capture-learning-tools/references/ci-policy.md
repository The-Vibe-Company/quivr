# CI Policy

Use this reference before recommending CI changes.

## Gold Standard

CI should protect the project without becoming a tax. The right check runs at the cheapest point where it still catches the issue in time.

## Repository Cost Modes

### Public open-source repository

Public repositories can usually afford stronger default CI on standard hosted runners. Recommend:

- PR fast checks for lint, typecheck, unit tests, and changed-package checks
- required checks on protected branches
- broader integration/e2e checks when project risk justifies it
- caching dependencies by lockfile
- minimal permissions and pinned actions where possible

### Private repository

Private repositories may consume paid minutes or paid runners. Recommend budget-aware CI:

- local preflight script first
- fast PR checks only for high-signal validations
- path filters so unrelated changes do not run expensive jobs
- scheduled or manual deep checks for slow suites
- release gates for deployment-sensitive checks
- explicit user approval before adding long or paid workflows

### Prototype or solo project

Prefer:

- one documented local command
- a lightweight smoke test
- a checklist in project instructions
- CI only when the project is shared, deployed, or breaking repeatedly

### Production, security, or compliance-sensitive project

Prefer stronger gates:

- required checks
- security scanning where relevant
- migration checks
- deploy previews or smoke tests
- protected workflow files

## CI Tiering

| Tier | Runs when | Good for | Watch out for |
| --- | --- | --- | --- |
| Local preflight | before final answer or PR | cheap, fast, private repos | depends on humans/agents remembering |
| PR fast checks | every pull request | lint, typecheck, unit tests | must stay fast |
| Path-scoped checks | matching files changed | docs/content/packages | path filters can miss cross-cutting changes |
| Nightly deep checks | scheduled | slow e2e, fuzz, full matrix | delayed feedback |
| Release gate | before deploy/tag | migrations, smoke, security | can block urgent releases |

## Recommendation Format

Every CI proposal should include:

- trigger: PR, push, path-scoped, scheduled, manual, release
- command
- estimated cost: low, medium, high
- why CI is better than local-only
- fallback if the user does not want CI cost

## Security Baseline

- Use least-privilege `permissions`.
- Keep secrets out of logs.
- Avoid running untrusted pull request code with write tokens.
- Cache dependencies by lockfile, not broad paths alone.
- Consider CODEOWNERS or review requirements for workflow changes.

## References

- GitHub Actions billing: https://docs.github.com/en/billing/concepts/product-billing/github-actions
- GitHub workflow syntax: https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax
- GitHub Actions dependency caching: https://docs.github.com/en/actions/reference/workflows-and-actions/dependency-caching
- GitHub Actions secure use: https://docs.github.com/en/actions/reference/security/secure-use
