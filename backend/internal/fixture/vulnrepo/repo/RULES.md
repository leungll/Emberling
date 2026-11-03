# Project rules

These rules apply to every change made to this service, by a person or by an automated
agent.

## Scope of a change

- Application code lives under `internal/`. A dependency or security fix is expected to
  touch `go.mod` and the package that uses the dependency, nothing else.
- The test suite under `tests/` is the acceptance contract of the service. It is not part
  of a fix: a change must make the existing tests pass, never alter them.
- The HTTP layer under `internal/server/` depends only on the exported API of
  `internal/auth/`. Keep that API stable so a fix inside `internal/auth/` needs no change
  elsewhere.

## Dependencies

- Builds run offline against the internal module mirror. A dependency version that the
  mirror does not serve cannot be built or tested.
- When a dependency must be upgraded, use the latest major version available in the
  internal mirror, and the latest release of that major. Do not stop at an intermediate
  major version.
- Remove the requirement of the old version from `go.mod` once nothing imports it. A
  `go.mod` that still requires a vulnerable version fails the dependency check even when
  the code no longer imports it.
- `go.sum` already lists the checksums of every module version the mirror serves, so a
  change to `go.mod` alone is enough; no network access or `go mod tidy` is needed.

## Mirror inventory

The internal mirror currently serves these module versions:

| Module | Versions |
|---|---|
| `github.com/golang-jwt/jwt` | `v3.2.2+incompatible` |
| `github.com/golang-jwt/jwt/v5` | `v5.3.1` |

## Verification

Every change is verified by three independent checks, all of which must pass:

1. the full test suite of the base commit (`go test ./...`),
2. a vulnerability scan against the pinned vulnerability database snapshot,
3. a comparison of the complete dependency list, including indirect dependencies,
   against the versions the snapshot marks as affected.
