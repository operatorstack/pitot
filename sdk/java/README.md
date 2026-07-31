# Pitot Java SDK — preview (not yet runnable)

**Status: types-only preview. Not a shipping SDK.** These files are the generated
Java data types for the Pitot wire protocol (`com.operatorstack.pitot.*`), emitted
by [`scripts/generate_types.sh`](../../../scripts/generate_types.sh) from the same
schema as every other binding. They let you *read the shapes* — they do **not** yet
give you a runnable Controller or Consumer.

What is **not** here yet, and what a first-class SDK requires:

- no build manifest (`pom.xml` / `build.gradle`), so the package does not build;
- no `runner` (the stdio JSON-Lines Controller/Consumer loop the other SDKs ship);
- no entry in the cross-language conformance suite
  ([`../tests/test_runners.py`](../tests/test_runners.py)), which is what proves a
  binding's wire behavior is identical to the others;
- not covered by the version-lockstep gate — it carries no release version.

The first-class, runnable SDKs are **Python**, **TypeScript**, **Go**, and
**Rust**. Use one of those to build against Pitot today; see each SDK's `README.md`
for a quickstart. This preview will graduate only once it clears the same bar: a
runner, a conformance entry, a build, and version lockstep.
