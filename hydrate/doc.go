// Package hydrate makes the Pitot binary a repository property: a committed
// pin (.pitot/version) names the exact version, and hydration materializes it
// into a write-once user cache from the distribution front door. Fresh clones,
// CI, and cloud agents need no install step — the first invocation hydrates.
//
// The package is governed by these control laws; each has a tagged
// conformance test (// control-law: <slug>):
//
//   - pin-is-the-only-version-authority — nothing resolves "latest" at exec
//     time; Ensure takes the pinned version as its only version input. Only
//     `pitot upgrade` may consult Latest.
//   - nothing-executes-unverified — the sha256 from checksums.txt must match
//     before a byte lands in the slot; a failed verify leaves no slot.
//   - slots-are-write-once — a populated slot is never rewritten; rollback is
//     reverting the pin (a cache hit).
//   - upgrade-is-a-reviewed-data-diff — the only repository mutation an
//     upgrade makes is rewriting .pitot/version; binaries change only as a
//     consequence of the committed pin.
//   - upgrade-preserves-the-tenancy-contract — an upgrade re-validates every
//     tenant fragment (merge + requires_protocol) before rewriting the pin.
//   - absence-fails-closed-and-named — no cache hit plus no network (or
//     PITOT_NO_HYDRATE=1) is a specific error naming the pin, the missing
//     slot, and the kill switch; there is no fallback to a PATH binary.
//   - the-shim-carries-no-policy — the repo shim reads the pin, fetches,
//     verifies, and execs; it never reads fragments or makes decisions.
//   - bindings-move-in-lockstep-with-the-CLI — one release tag ships binary,
//     npm, and python bindings at one version; installs pin exactly.
//   - packages-come-from-our-registry-or-nowhere — consumers resolve
//     bindings only through the front door host; registry internals never
//     appear in client config.
package hydrate
