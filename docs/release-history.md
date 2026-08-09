# Release history

## `v0.1.0-preview.1`

The immutable annotated tag points to commit
`e06c0ff48da9244f2f7c04495a50ad4f9aaedef1`. Release workflow run
`31333877865` stopped in its unprivileged candidate-validation job before
central rendering, independent verification, attestation, or publication.

The candidate had no `verify-release` Make target even though the reusable
distribution workflow requires that repository-owned offline gate. No release
artifacts or GitHub Release were created. The tag is retained unchanged as
failed-release history. Preview 2 adds the exact alias and a repository quality
regression that prevents another release candidate from omitting it.
