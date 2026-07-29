#ifndef LIBGITLFS_H
#define LIBGITLFS_H

#ifdef __cplusplus
extern "C" {
#endif

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

/**
 * Lock a file.
 * Returns 0 on success. On error, returns non-zero and allocates errorMsg if provided.
 */
extern int GitLFS_Lock(char* repoPath, char* filePath, char** errorMsg);

/**
 * Unlock a file.
 * Returns 0 on success. On error, returns non-zero and allocates errorMsg if provided.
 */
extern int GitLFS_Unlock(char* repoPath, char* filePath, int force, char** errorMsg);

/**
 * Search and retrieve locks for the given repository.
 * Returns a GitLFSLockList. If Count is 0 and errorMsg is set, an error occurred.
 * The returned list MUST be freed with GitLFS_FreeLocks.
 */
extern GitLFSLockList GitLFS_Locks(char* repoPath, char** errorMsg);

/**
 * Frees the memory allocated by GitLFS_Locks.
 */
extern void GitLFS_FreeLocks(GitLFSLockList list);

/**
 * Frees the memory allocated for errorMsg by other GitLFS API functions.
 */
extern void GitLFS_FreeError(char* errMsg);

#ifdef __cplusplus
}
#endif

#endif // LIBGITLFS_H
