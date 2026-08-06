# Contributing

Use Go 1.26.5 and work in a clean checkout. Keep changes small, preserve the
dependency direction in `ARCHITECTURE.md`, and add tests for success, invalid
configuration, cancellation, partial streams, and redaction when relevant.

Run:

```text
make fast
make check
make verify
```

`make verify` is the local merge gate. Update the dependency and security
reviews whenever a dependency, network behavior, credential surface, retry
policy, or observable metadata changes.
