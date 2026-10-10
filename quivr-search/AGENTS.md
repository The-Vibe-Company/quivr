# Demo test ownership

Follow the [testing standard](../docs/agents/testing.md) before changing tests.

- Facet rules belong in `tests/explore.test.mjs` through `createExplorer().facets`; supply raw engine answers and literal corpus schemas at its upstream boundary. Keep date/count helpers private.
- Use independent literal period bounds and distinct day identities. Compare complete outgoing predicates because bucket fixtures do not evaluate filters.
- Check concurrency and batch caps as upper bounds, with complete work coverage; a smaller legal batch must pass.
- Browser fixtures prove client rendering and interaction; facade tests prove transport and scope. Keep separate literal arithmetic owners when fixtures reuse production helpers.
- Name the assertions observed: published aggregates do not identify one write, and screenshots do not prove internal layout fit.
