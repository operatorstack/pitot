# Contributing to Pitot

Pitot is protocol-first infrastructure. Contributions should preserve the
separation between observation, controller authority, and host transport.

Before proposing an adapter or protocol change:

1. state the host capability or interoperability gap;
2. add or update language-neutral conformance fixtures;
3. preserve direct, estimated, and unavailable observation provenance;
4. keep content out of fault codes and diagnostics;
5. prove passive Consumers cannot reach the response channel; and
6. document any change to request correlation or timeout behavior.

Implementation instructions will be added with the first buildable release.

