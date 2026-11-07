# vulnrepo: the example repository of the vulnerability-fix demo

`repo/` is a small Go HTTP service, the token service, whose `go.mod` requires
`github.com/golang-jwt/jwt v3.2.2+incompatible`. That version is affected by the public
advisory GO-2025-3553, which has no fix on the `github.com/golang-jwt/jwt` module path. The
fix is a move to `github.com/golang-jwt/jwt/v5`, whose API differs: `StandardClaims` and
`ValidationError` are gone, so upgrading the version alone breaks the build and the calling
code in `internal/auth/` must be adapted. That is what gives an Agent a real
upgrade-fail-adapt-verify loop to work through.

## Layout

| Path | Role | May a fix change it? |
|---|---|---|
| `go.mod`, `go.sum` | module definition and checksums | `go.mod` yes; `go.sum` already lists every version the mirror serves |
| `internal/auth/` | token issue and verification, the only package that imports the library | yes, this is the allowed directory |
| `internal/server/` | HTTP layer, depends only on the exported API of `internal/auth` | no |
| `cmd/tokenservice/` | the binary | no |
| `tests/` | the acceptance suite, in its own package outside the code it tests | no: the suite is the contract a fix must keep passing |
| `RULES.md` | project rules the Agent reads, including the dependency-upgrade rule and the mirror inventory | no |
| `advisories/GO-2025-3553.md` | the advisory the task names, with affected and fixed versions | no |

The tests live under `tests/` rather than next to the code so that a Definition can allow
writes under `internal/auth/` and `go.mod` without allowing any change to a test. The
verification runner always runs the tests of the base commit, so the suite cannot be
weakened by a patch.

## Offline builds

The runner builds and tests with `GOPROXY=off` and `GOFLAGS=-mod=readonly` against a module
cache that holds exactly two versions: `github.com/golang-jwt/jwt v3.2.2+incompatible` and
`github.com/golang-jwt/jwt/v5 v5.3.1`. `go.sum` already carries the checksums of both, so a
patch that only edits `go.mod` passes module verification without running `go mod tidy`,
which an Agent without a shell could not do. A `go.mod` that names any other version, such
as `github.com/golang-jwt/jwt/v4`, fails as a missing module and the reason is reported.

## Generating the repository

```sh
sh backend/internal/fixture/vulnrepo/generate.sh /path/to/vuln-demo-repo
```

creates a git repository with one commit, tagged `baseline`, and prints its id. The commit
is reproducible, so every generated copy has the same id; register the path under
`EMBERLING_WORKSPACE_REPOSITORIES` and name that id as the workspace `baseCommit`. Running
the script again leaves a target that already holds the baseline untouched and replaces
anything else.

## Fixture patches

`testdata/` holds cumulative patches against the baseline, in the form `git diff` writes
them, for the runner tests and for scripted Agent trajectories:

| Patch | What it changes | Expected verification |
|---|---|---|
| `fix-gomod-only.patch` | `go.mod` requires `/v5` only | module verification passes; the build fails because the code still imports the old path |
| `upgrade-without-adaptation.patch` | `go.mod` and the import path move to `/v5`, the code is unchanged | compile error: `jwt.StandardClaims` no longer exists |
| `fix-complete.patch` | `go.mod` and `internal/auth/token.go` moved to `/v5` with the API adapted | tests pass, scan clean, dependencies clean |
| `fix-v4.patch` | `go.mod` requires `/v4 v4.5.2` | fails: the module is not in the offline cache |
| `keep-old-require.patch` | the complete fix, but `go.mod` still requires the old version | tests pass, but the dependency list still holds the vulnerable version |

The sandbox runner's mock verification backend, which runs nothing, gives the same verdict
as the real build for the one failure the scripted demo relies on: a patch that requires
the new major version while the code still uses the removed claims type is reported as a
failed test step. The scripted `vuln-fix-main-verify` trajectory writes the
`upgrade-without-adaptation` state first and the complete fix after that failure, so it
holds against the mock runner and the docker runner alike.
