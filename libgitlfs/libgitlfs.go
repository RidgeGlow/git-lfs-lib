//go:build libgitlfs

package main

/*
#include <stdlib.h>
#include <string.h>

#include "libgitlfs.h"
*/
import "C"

import (
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"github.com/git-lfs/git-lfs/v3/config"
	"github.com/git-lfs/git-lfs/v3/git"
	"github.com/git-lfs/git-lfs/v3/lfsapi"
	"github.com/git-lfs/git-lfs/v3/locking"
	"github.com/git-lfs/git-lfs/v3/tools"
)

func main() {
	// Empty main function is required for buildmode=c-archive
}

// session bundles everything one call needs, so that a bulk operation pays the
// setup cost once rather than once per path.
type session struct {
	cfg        *config.Configuration
	apiClient  *lfsapi.Client
	lockClient *locking.Client
}

func (s *session) Close() {
	if s.lockClient != nil {
		s.lockClient.Close()
	}
}

// setErr stores msg into *errorMsg when the caller asked for it. Callers of
// this API are not required to pre-initialise errorMsg, so every entry point
// clears it first; otherwise a caller checking errorMsg after a successful
// call would read whatever was already on their stack.
func setErr(errorMsg **C.char, msg string) {
	if errorMsg != nil {
		*errorMsg = C.CString(msg)
	}
}

func clearErr(errorMsg **C.char) {
	if errorMsg != nil {
		*errorMsg = nil
	}
}

func newSession(rPath string, errorMsg **C.char) (*session, error) {
	cfg := config.NewIn(rPath, "")
	apiClient := lfsapi.NewClient(cfg)

	refUpdate := git.NewRefUpdate(cfg.Git, cfg.PushRemote(), cfg.CurrentRef(), nil)
	lockClient := locking.NewClient(cfg.PushRemote(), apiClient, cfg)

	if err := tools.MkdirAll(cfg.LFSStorageDir(), cfg); err != nil {
		lockClient.Close()
		setErr(errorMsg, err.Error())
		return nil, err
	}
	if err := lockClient.SetupFileCache(cfg.LFSStorageDir()); err != nil {
		lockClient.Close()
		setErr(errorMsg, err.Error())
		return nil, err
	}

	lockClient.LocalWorkingDir = cfg.LocalWorkingDir()
	lockClient.LocalGitDir = cfg.LocalGitDir()
	lockClient.SetLockableFilesReadOnly = cfg.SetLockableFilesReadOnly()
	lockClient.RemoteRef = refUpdate.RemoteRef()

	return &session{cfg: cfg, apiClient: apiClient, lockClient: lockClient}, nil
}

// repoRelative converts an absolute or relative path into the repository-relative,
// forward-slashed form the locking API expects.
func (s *session) repoRelative(fPath string) (string, error) {
	if !filepath.IsAbs(fPath) {
		// Already relative; assume it is relative to the repository root.
		return filepath.ToSlash(fPath), nil
	}
	rel, err := filepath.Rel(s.cfg.LocalWorkingDir(), fPath)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

// goStrings converts a C array of C strings into a Go slice.
func goStrings(argv **C.char, count int) []string {
	if argv == nil || count <= 0 {
		return nil
	}
	cSlice := unsafe.Slice(argv, count)
	out := make([]string, count)
	for i, s := range cSlice {
		out[i] = C.GoString(s)
	}
	return out
}

// forEachConcurrent runs fn for each index in [0,n), at most limit at a time.
func forEachConcurrent(n, limit int, fn func(i int)) {
	if limit < 1 {
		limit = 1
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			fn(i)
		}(i)
	}
	wg.Wait()
}

// newPathResults allocates a C array of GitLFSPathResult, or reports failure if
// the allocation could not be made.
func newPathResults(count int) (C.GitLFSPathResultList, []C.GitLFSPathResult, bool) {
	var empty C.GitLFSPathResultList
	if count <= 0 {
		return empty, nil, false
	}
	mem := C.malloc(C.size_t(count) * C.size_t(unsafe.Sizeof(C.GitLFSPathResult{})))
	if mem == nil {
		return empty, nil, false
	}
	arr := (*C.GitLFSPathResult)(mem)
	return C.GitLFSPathResultList{Results: arr, Count: C.int(count)},
		unsafe.Slice(arr, count), true
}

// bulk is the shared body of GitLFS_LockMany and GitLFS_UnlockMany.
func bulk(repoPath *C.char, filePaths **C.char, count C.int, errorMsg **C.char,
	op func(s *session, rel string) error) C.GitLFSPathResultList {

	var empty C.GitLFSPathResultList
	clearErr(errorMsg)

	paths := goStrings(filePaths, int(count))
	if len(paths) == 0 {
		return empty
	}

	s, err := newSession(C.GoString(repoPath), errorMsg)
	if err != nil {
		return empty
	}
	defer s.Close()

	list, slice, ok := newPathResults(len(paths))
	if !ok {
		setErr(errorMsg, "failed to allocate result array")
		return empty
	}

	// Each goroutine writes only to its own index, so the C array needs no
	// additional synchronisation.
	forEachConcurrent(len(paths), s.apiClient.ConcurrentTransfers(), func(i int) {
		slice[i].Path = C.CString(paths[i])
		slice[i].Success = 0
		slice[i].Error = nil

		rel, err := s.repoRelative(paths[i])
		if err != nil {
			slice[i].Error = C.CString(err.Error())
			return
		}
		if err := op(s, rel); err != nil {
			slice[i].Error = C.CString(err.Error())
			return
		}
		slice[i].Success = 1
	})

	return list
}

//export GitLFS_Lock
func GitLFS_Lock(repoPath *C.char, filePath *C.char, errorMsg **C.char) C.int {
	clearErr(errorMsg)

	s, err := newSession(C.GoString(repoPath), errorMsg)
	if err != nil {
		return 1
	}
	defer s.Close()

	rel, err := s.repoRelative(C.GoString(filePath))
	if err != nil {
		setErr(errorMsg, err.Error())
		return 1
	}
	if _, err := s.lockClient.LockFile(rel); err != nil {
		setErr(errorMsg, err.Error())
		return 1
	}
	return 0
}

//export GitLFS_Unlock
func GitLFS_Unlock(repoPath *C.char, filePath *C.char, force C.int, errorMsg **C.char) C.int {
	clearErr(errorMsg)

	s, err := newSession(C.GoString(repoPath), errorMsg)
	if err != nil {
		return 1
	}
	defer s.Close()

	rel, err := s.repoRelative(C.GoString(filePath))
	if err != nil {
		setErr(errorMsg, err.Error())
		return 1
	}
	if err := s.lockClient.UnlockFile(rel, force != 0); err != nil {
		setErr(errorMsg, err.Error())
		return 1
	}
	return 0
}

//export GitLFS_LockMany
func GitLFS_LockMany(repoPath *C.char, filePaths **C.char, count C.int, errorMsg **C.char) C.GitLFSPathResultList {
	return bulk(repoPath, filePaths, count, errorMsg, func(s *session, rel string) error {
		_, err := s.lockClient.LockFile(rel)
		return err
	})
}

//export GitLFS_UnlockMany
func GitLFS_UnlockMany(repoPath *C.char, filePaths **C.char, count C.int, force C.int, errorMsg **C.char) C.GitLFSPathResultList {
	return bulk(repoPath, filePaths, count, errorMsg, func(s *session, rel string) error {
		return s.lockClient.UnlockFile(rel, force != 0)
	})
}

//export GitLFS_Locks
func GitLFS_Locks(repoPath *C.char, cached C.int, localOnly C.int, errorMsg **C.char) C.GitLFSLockList {
	var empty C.GitLFSLockList
	clearErr(errorMsg)

	s, err := newSession(C.GoString(repoPath), errorMsg)
	if err != nil {
		return empty
	}
	defer s.Close()

	locks, err := s.lockClient.SearchLocks(nil, 0, localOnly != 0, cached != 0)
	if err != nil {
		setErr(errorMsg, err.Error())
		return empty
	}

	count := len(locks)
	if count == 0 {
		return empty
	}

	mem := C.malloc(C.size_t(count) * C.size_t(unsafe.Sizeof(C.GitLFSLock{})))
	if mem == nil {
		setErr(errorMsg, "failed to allocate lock array")
		return empty
	}
	cArray := (*C.GitLFSLock)(mem)
	slice := unsafe.Slice(cArray, count)

	for i, lock := range locks {
		slice[i].Id = C.CString(lock.Id)
		slice[i].Path = C.CString(lock.Path)
		slice[i].LockedAt = C.CString(lock.LockedAt.Format(time.RFC3339))
		if lock.Owner != nil {
			slice[i].OwnerName = C.CString(lock.Owner.Name)
		} else {
			slice[i].OwnerName = nil
		}
	}

	return C.GitLFSLockList{Locks: cArray, Count: C.int(count)}
}

//export GitLFS_FreeLocks
func GitLFS_FreeLocks(list C.GitLFSLockList) {
	if list.Locks == nil || list.Count <= 0 {
		return
	}
	slice := unsafe.Slice(list.Locks, int(list.Count))
	for i := range slice {
		C.free(unsafe.Pointer(slice[i].Id))
		C.free(unsafe.Pointer(slice[i].Path))
		C.free(unsafe.Pointer(slice[i].LockedAt))
		C.free(unsafe.Pointer(slice[i].OwnerName))
	}
	C.free(unsafe.Pointer(list.Locks))
}

//export GitLFS_FreePathResults
func GitLFS_FreePathResults(list C.GitLFSPathResultList) {
	if list.Results == nil || list.Count <= 0 {
		return
	}
	slice := unsafe.Slice(list.Results, int(list.Count))
	for i := range slice {
		C.free(unsafe.Pointer(slice[i].Path))
		C.free(unsafe.Pointer(slice[i].Error))
	}
	C.free(unsafe.Pointer(list.Results))
}

//export GitLFS_FreeError
func GitLFS_FreeError(errMsg *C.char) {
	C.free(unsafe.Pointer(errMsg))
}
