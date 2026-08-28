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
| `.github/workflows/ci.yml` | upstream — **one** deliberate hunk, see below |
| everything else | upstream — do not modify |

`git-lfs.go` and `.github/workflows/release.yml` are byte-identical to upstream
and must stay that way. Syncing upstream should be a clean fast-forward for both.

`ci.yml` carries exactly one deviation, a single hunk at the top of the file
replacing upstream's `on: [push, pull_request]`. That line builds every branch
push twice, once per event, at ten jobs a run, and rebuilds for documentation-only
changes. In its place: `push` restricted to `main`, `paths-ignore` for markdown,
and a `concurrency` group so a new push supersedes the run it replaced.

Upstream's original line is recorded in a comment beside it, so a conflict is
resolved by taking upstream's version and re-applying the block. Keep it to that
one hunk — do not let edits to this file spread.

`library.yml` has the same concurrency group, but **no `paths-ignore` on its
push trigger**: that trigger carries the `lib-v*` tags that publish releases, and
a release commit touching only documentation would otherwise never build.
Cancellation is likewise limited to `pull_request` in both files, so a release or
a `main` build is never interrupted by whatever lands next.

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

The **shared library is the primary artifact**:

```sh
CGO_ENABLED=1 go build -buildmode=c-shared -tags libgitlfs \
  -o libgitlfs.so ./libgitlfs        # .dll on Windows, .dylib on macOS
```

The static archive is still built and published, so that option stays open:

```sh
CGO_ENABLED=1 go build -buildmode=c-archive -tags libgitlfs \
  -o libgitlfs.a ./libgitlfs
```

Prefer the shared library. Linking a Go `c-archive` into an MSVC-built program
is not a well-trodden path — the archive is a MinGW-produced GNU `ar` archive
whose objects carry libgcc/mingwex dependencies. With `c-shared` that CRT stays
*inside* the library and only a clean C ABI crosses the boundary, so a consumer
can load it with `dlopen`/`LoadLibrary` and never link against it at all.

`.github/workflows/library.yml` is the authoritative build. It also gates the
things that actually break a consumer at runtime: the dependency closure must be
system-only, the Linux `.so` is built in an `ubuntu:20.04` container against a
`GLIBC_2.31` floor, the macOS `.dylib` is a `lipo`'d universal binary pinned to
`MACOSX_DEPLOYMENT_TARGET=12.0` and ad-hoc signed, and every artifact must export
all eight entry points.

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

### Panics are contained

A Go panic that escapes a `//export`ed function aborts the **host process** —
there is no C++ exception for the caller to catch and no way for it to intervene.
Every entry point therefore recovers, and reports the panic through the channel
it already has: `errorMsg` for the single-file and whole-batch cases, or
`Success = 0` plus `Error` for one path inside a bulk call. The message carries
the Go stack, which is usually the only diagnostic available from a user's
machine.

The per-path workers inside `GitLFS_LockMany` / `GitLFS_UnlockMany` recover
separately, and must: they run on their own goroutines, where a panic cannot be
recovered by the exported function's `defer` and would abort the process
regardless.

This covers panics only. Go runtime *fatal* errors — concurrent map write, stack
exhaustion, out of memory, deadlock detection — and genuine segfaults bypass
`recover()` and still abort. Consumers should assume the library can, in
principle, take the process down, and be able to run without it.

### The runtime cannot be unloaded

Do not `dlclose` / `FreeLibrary` this library. The Go runtime does not support
being unloaded or reinitialised: its threads persist, and loading again returns
the same handle rather than a fresh runtime. Load once and leak the handle
deliberately at shutdown.

Nothing is lost by this: every call builds its own configuration and client, so
there is no cross-call state that a reload would clear.
