# Contributing

Use Go 1.26.5. Keep changes within the ownership boundaries in
`ARCHITECTURE.md`, add tests with behavior, and avoid speculative public APIs.

Run:

```text
make fast
make check
make verify
```

`make verify` is the commit gate. Dependency additions require an update to
`docs/dependency-review.md` covering maintenance, license, supply-chain risk,
cancellation, observability, and platform behavior.
