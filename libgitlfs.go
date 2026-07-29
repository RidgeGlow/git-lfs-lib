//go:build libgitlfs

package main

/*
#include <stdlib.h>
#include <string.h>

typedef struct {
    char* Id;
    char* Path;
    char* LockedAt;
    char* OwnerName;
} GitLFSLock;

typedef struct {
    GitLFSLock* Locks;
    int Count;
} GitLFSLockList;
*/
import "C"

import (
	"path/filepath"
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

func setupLockClient(rPath string, errorMsg **C.char) (*config.Configuration, *locking.Client, error) {
	cfg := config.NewIn(rPath, "")
	apiClient := lfsapi.NewClient(cfg)
	
	refUpdate := git.NewRefUpdate(cfg.Git, cfg.PushRemote(), cfg.CurrentRef(), nil)
	lockClient := locking.NewClient(cfg.PushRemote(), apiClient, cfg)
	
	tools.MkdirAll(cfg.LFSStorageDir(), cfg)
	if err := lockClient.SetupFileCache(cfg.LFSStorageDir()); err != nil {
		if errorMsg != nil {
			*errorMsg = C.CString(err.Error())
		}
		return nil, nil, err
	}

	lockClient.LocalWorkingDir = cfg.LocalWorkingDir()
	lockClient.LocalGitDir = cfg.LocalGitDir()
	lockClient.SetLockableFilesReadOnly = cfg.SetLockableFilesReadOnly()
	lockClient.RemoteRef = refUpdate.RemoteRef()

	return cfg, lockClient, nil
}

//export GitLFS_Lock
func GitLFS_Lock(repoPath *C.char, filePath *C.char, errorMsg **C.char) C.int {
	rPath := C.GoString(repoPath)
	fPath := C.GoString(filePath)

	cfg, lockClient, err := setupLockClient(rPath, errorMsg)
	if err != nil {
		return 1
	}
	defer lockClient.Close()

	// Convert fPath (absolute or relative) into a path relative to the repo root
	var relPath string
	if filepath.IsAbs(fPath) {
		relPath, err = filepath.Rel(cfg.LocalWorkingDir(), fPath)
		if err != nil {
			if errorMsg != nil {
				*errorMsg = C.CString(err.Error())
			}
			return 1
		}
	} else {
		// If already relative, assuming it's relative to the repo root for this API
		relPath = fPath
	}

	_, err = lockClient.LockFile(filepath.ToSlash(relPath))
	if err != nil {
		if errorMsg != nil {
			*errorMsg = C.CString(err.Error())
		}
		return 1
	}
	return 0
}

//export GitLFS_Unlock
func GitLFS_Unlock(repoPath *C.char, filePath *C.char, force C.int, errorMsg **C.char) C.int {
	rPath := C.GoString(repoPath)
	fPath := C.GoString(filePath)

	cfg, lockClient, err := setupLockClient(rPath, errorMsg)
	if err != nil {
		return 1
	}
	defer lockClient.Close()

	var relPath string
	if filepath.IsAbs(fPath) {
		relPath, err = filepath.Rel(cfg.LocalWorkingDir(), fPath)
		if err != nil {
			if errorMsg != nil {
				*errorMsg = C.CString(err.Error())
			}
			return 1
		}
	} else {
		relPath = fPath
	}

	err = lockClient.UnlockFile(filepath.ToSlash(relPath), force != 0)
	if err != nil {
		if errorMsg != nil {
			*errorMsg = C.CString(err.Error())
		}
		return 1
	}
	return 0
}

//export GitLFS_Locks
func GitLFS_Locks(repoPath *C.char, errorMsg **C.char) C.GitLFSLockList {
	var emptyList C.GitLFSLockList
	rPath := C.GoString(repoPath)

	_, lockClient, err := setupLockClient(rPath, errorMsg)
	if err != nil {
		return emptyList
	}
	defer lockClient.Close()

	// Get all locks
	locks, err := lockClient.SearchLocks(nil, 0, false, false)
	if err != nil {
		if errorMsg != nil {
			*errorMsg = C.CString(err.Error())
		}
		return emptyList
	}

	count := len(locks)
	if count == 0 {
		return emptyList
	}

	// Allocate array of GitLFSLock in C
	cArray := (*C.GitLFSLock)(C.malloc(C.size_t(count) * C.size_t(unsafe.Sizeof(C.GitLFSLock{}))))
	slice := (*[1 << 30]C.GitLFSLock)(unsafe.Pointer(cArray))[:count:count]

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

	return C.GitLFSLockList{
		Locks: cArray,
		Count: C.int(count),
	}
}

//export GitLFS_FreeLocks
func GitLFS_FreeLocks(list C.GitLFSLockList) {
	if list.Locks == nil || list.Count == 0 {
		return
	}

	count := int(list.Count)
	slice := (*[1 << 30]C.GitLFSLock)(unsafe.Pointer(list.Locks))[:count:count]

	for i := 0; i < count; i++ {
		if slice[i].Id != nil {
			C.free(unsafe.Pointer(slice[i].Id))
		}
		if slice[i].Path != nil {
			C.free(unsafe.Pointer(slice[i].Path))
		}
		if slice[i].LockedAt != nil {
			C.free(unsafe.Pointer(slice[i].LockedAt))
		}
		if slice[i].OwnerName != nil {
			C.free(unsafe.Pointer(slice[i].OwnerName))
		}
	}

	C.free(unsafe.Pointer(list.Locks))
}

//export GitLFS_FreeError
func GitLFS_FreeError(errMsg *C.char) {
	if errMsg != nil {
		C.free(unsafe.Pointer(errMsg))
	}
}
