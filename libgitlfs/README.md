# libgitlfs

A C-compatible static library exposing Git LFS functionality, so programs can
interact with Git LFS directly instead of shelling out to the `git-lfs`
executable.

## Design constraint: zero modified upstream files

This fork tracks `git-lfs/git-lfs` and must stay easy to sync. Every upstream
file we edit becomes a merge conflict on every future sync, so the library is
built entirely from **additive** files:

| Path | Owner |
|---|---|
| `libgitlfs/` | ours |
| `.github/workflows/library.yml` | ours |
| everything else | upstream — do not modify |

Notably `git-lfs.go`, `.github/workflows/ci.yml`, and
`.github/workflows/release.yml` are byte-identical to upstream and must stay
that way. Syncing upstream should be a clean fast-forward for all of them.

This is why the library lives in its own package directory rather than at the
repository root: a root-level `libgitlfs.go` would collide with `git-lfs.go`'s
`main()` and force a build tag onto an upstream file.

It also avoids a subtle bug. Go links any `*.syso` found in the package
directory being built. The repository root accumulates a `resource.syso` from
`goversioninfo` whose architecture is whatever was built last — on Windows CI
that is `arm64`. A root-level archive silently absorbs that mismatched object
and fails to link downstream with `LNK1112`. Building `./libgitlfs` never sees
it.

## Building

```sh
CGO_ENABLED=1 go build -buildmode=c-archive -tags libgitlfs \
  -o libgitlfs.a ./libgitlfs
```

The `libgitlfs` build tag keeps this package out of `go build ./...`, so a
checkout without a C toolchain still builds normally.

cgo requires a **gcc-compatible** compiler. On Windows that means the MinGW
toolchain from the Git for Windows SDK; MSVC's `cl.exe` cannot drive cgo.

Two headers are produced: the hand-written `libgitlfs.h` (struct definitions)
and a cgo-generated header alongside the archive that `#include`s it. Ship both,
kept in the same directory.

## Consuming

The C API is declared in `libgitlfs.h`. It covers the locking surface that
[RidgeGlowUnrealGit](https://github.com/RidgeGlow/RidgeGlowUnrealGit) currently
gets by shelling out to a bundled `git-lfs` binary — that plugin uses the
bundled binary *only* for locking (`lock`, `unlock`, `locks`); everything else
goes through plain `git`.

| Need | Entry point |
|---|---|
| `git lfs lock <path>` | `GitLFS_Lock` |
| `git lfs unlock <path>` | `GitLFS_Unlock` |
| `git lfs lock <many paths>` | `GitLFS_LockMany` |
| `git lfs unlock <many paths>` | `GitLFS_UnlockMany` |
| `git lfs locks` | `GitLFS_Locks(cached=0, localOnly=0)` |
| `git lfs locks --cached` | `GitLFS_Locks(cached=1, localOnly=0)` |
| `git lfs locks --local` | `GitLFS_Locks(cached=0, localOnly=1)` |

### Bulk operations

Use `GitLFS_LockMany` / `GitLFS_UnlockMany` for anything touching more than one
file, such as locking a whole directory. They build the configuration, API
client, and lock cache **once** and then issue the requests concurrently, bounded
by `lfs.concurrenttransfers`. Looping over the single-file entry points instead
would repeat that setup per file and serialise every request.

The CLI needed a `MaxFilesPerBatch` chunking dance to stay under command-line
length limits. That constraint does not exist here: pass the whole list.

Bulk calls are **partial-success**. `errorMsg` is set only when the entire batch
could not start (for example the repository could not be opened), in which case
`Count` is 0. Otherwise every path gets an entry and you must inspect each
`Success` field.

### Memory and error handling

Every function returning allocated memory has a matching free function:
`GitLFS_FreeLocks`, `GitLFS_FreePathResults`, and `GitLFS_FreeError`.

`errorMsg` is cleared to `NULL` on entry to every function, so it is safe to
check after a call without pre-initialising it.

Note that the Windows archive is produced by MinGW. Linking a Go `c-archive`
into an MSVC-built program is not a well-trodden path; if the consumer is MSVC
(for example Unreal Engine), evaluate `-buildmode=c-shared` and loading the
resulting DLL instead.
