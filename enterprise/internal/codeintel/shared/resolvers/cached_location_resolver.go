package sharedresolvers

import (
	"context"
	"sync"

	"github.com/sourcegraph/log"

	"github.com/sourcegraph/sourcegraph/cmd/frontend/backend"
	"github.com/sourcegraph/sourcegraph/internal/api"
	resolverstubs "github.com/sourcegraph/sourcegraph/internal/codeintel/resolvers"
	"github.com/sourcegraph/sourcegraph/internal/database"
	"github.com/sourcegraph/sourcegraph/internal/errcode"
	"github.com/sourcegraph/sourcegraph/internal/gitserver"
	"github.com/sourcegraph/sourcegraph/internal/gitserver/gitdomain"
	"github.com/sourcegraph/sourcegraph/lib/errors"
)

// CachedLocationResolver resolves repositories, commits, and git tree entries and caches the resulting
// resolvers so that the same request does not re-resolve the same repository, commit, or path multiple
// times during execution. This cache reduces the number duplicate of database and gitserver queries for
// definition, reference, and diagnostic queries, which return collections of results that often refer
// to a small set of repositories, commits, and paths with a large multiplicity.
//
// This resolver maintains a hierarchy of caches as a way to decrease lock contention. Resolution of a
// repository holds the top-level lock. Resolution of a commit holds a lock associated with the parent
// repository. Similarly, resolution of a path holds a lock associated with the parent commit.
type CachedLocationResolver struct {
	sync.RWMutex
	children        map[api.RepoID]*cachedRepositoryResolver
	db              database.DB
	gitserverClient gitserver.Client
	logger          log.Logger
}

type cachedRepositoryResolver struct {
	sync.RWMutex
	resolver *RepositoryResolver
	children map[string]*cachedCommitResolver
}

type cachedCommitResolver struct {
	sync.RWMutex
	resolver *GitCommitResolver
	children map[string]*GitTreeEntryResolver
}

// NewCachedLocationResolver creates a location resolver with an empty cache.
func NewCachedLocationResolver(db database.DB, gitserverClient gitserver.Client) *CachedLocationResolver {
	return &CachedLocationResolver{
		logger:          log.Scoped("CachedLocationResolver", ""),
		db:              db,
		gitserverClient: gitserverClient,
		children:        map[api.RepoID]*cachedRepositoryResolver{},
	}
}

// Repository resolves the repository with the given identifier. This method may return a nil resolver
// if the repository is not known by gitserver - this happens if there is exists still a bundle for a
// repo that has since been deleted.
func (r *CachedLocationResolver) Repository(ctx context.Context, id api.RepoID) (*RepositoryResolver, error) {
	cachedRepositoryResolver, err := r.cachedRepository(ctx, id)
	if err != nil || cachedRepositoryResolver == nil {
		return nil, err
	}
	return cachedRepositoryResolver.resolver, nil
}

// Commit resolves the git commit with the given repository identifier and commit hash. This method may
// return a nil resolver if the commit is not known by gitserver.
func (r *CachedLocationResolver) Commit(ctx context.Context, id api.RepoID, commit string) (*GitCommitResolver, error) {
	cachedCommitResolver, err := r.cachedCommit(ctx, id, commit)
	if err != nil || cachedCommitResolver == nil {
		return nil, err
	}
	return cachedCommitResolver.resolver, nil
}

// Path resolves the git tree entry with the given repository identifier, commit hash, and relative path.
// This method may return a nil resolver if the commit is not known by gitserver.
func (r *CachedLocationResolver) Path(ctx context.Context, id api.RepoID, commit, path string) (*GitTreeEntryResolver, error) {
	pathResolver, err := r.cachedPath(ctx, id, commit, path)
	if err != nil {
		return nil, err
	}
	return pathResolver, nil
}

// cachedRepository resolves the repository with the given identifier if the resulting resolver does not
// already exist in the cache. The cache is tested/populated with double-checked locking, which ensures
// that the resolver is created exactly once per GraphQL request.
//
// See https://en.wikipedia.org/wiki/Double-checked_locking.
func (r *CachedLocationResolver) cachedRepository(ctx context.Context, id api.RepoID) (*cachedRepositoryResolver, error) {
	// Fast-path cache check
	r.RLock()
	cr, ok := r.children[id]
	r.RUnlock()
	if ok {
		return cr, nil
	}

	r.Lock()
	defer r.Unlock()

	// Check again once locked to avoid race
	if resolver, ok := r.children[id]; ok {
		return resolver, nil
	}

	// Resolve new value and store in cache
	resolver, err := r.resolveRepository(ctx, id)
	if err != nil {
		return nil, err
	}

	// Ensure value written to the cache is nil and not a nil resolver wrapped
	// in a non-nil cached commit resolver. Otherwise, a subsequent resolution
	// of a path may result in a nil dereference.
	var cachedResolver *cachedRepositoryResolver
	if resolver != nil {
		cachedResolver = &cachedRepositoryResolver{resolver: resolver, children: map[string]*cachedCommitResolver{}}
	}
	r.children[id] = cachedResolver
	return cachedResolver, nil
}

// cachedCommit resolves the commit with the given repository identifier and commit hash if the resulting
// resolver does not already exist in the cache. The cache is tested/populated with double-checked locking,
// which ensures that the resolver is created exactly once per GraphQL request.
//
// See https://en.wikipedia.org/wiki/Double-checked_locking.
func (r *CachedLocationResolver) cachedCommit(ctx context.Context, id api.RepoID, commit string) (*cachedCommitResolver, error) {
	parentResolver, err := r.cachedRepository(ctx, id)
	if err != nil || parentResolver == nil {
		return nil, err
	}

	// Fast-path cache check
	parentResolver.RLock()
	cr, ok := parentResolver.children[commit]
	parentResolver.RUnlock()
	if ok {
		return cr, nil
	}

	parentResolver.Lock()
	defer parentResolver.Unlock()

	// Check again once locked to avoid race
	if resolver, ok := parentResolver.children[commit]; ok {
		return resolver, nil
	}

	// Resolve new value and store in cache
	resolver, err := r.resolveCommit(ctx, parentResolver.resolver, commit)
	if err != nil {
		return nil, err
	}
	// Ensure value written to the cache is nil and not a nil resolver wrapped
	// in a non-nil cached commit resolver. Otherwise, a subsequent resolution
	// of a path may result in a nil dereference.
	var cachedResolver *cachedCommitResolver
	if resolver != nil {
		cachedResolver = &cachedCommitResolver{resolver: resolver, children: map[string]*GitTreeEntryResolver{}}
	}
	parentResolver.children[commit] = cachedResolver
	return cachedResolver, nil
}

// cachedPath resolves the commit with the given repository identifier, commit hash, and relative path
// if the resulting resolver does not already exist in the cache. The cache is tested/populated with
// double-checked locking, which ensures that the resolver is created exactly once per GraphQL request.
//
// See https://en.wikipedia.org/wiki/Double-checked_locking.
func (r *CachedLocationResolver) cachedPath(ctx context.Context, id api.RepoID, commit, path string) (*GitTreeEntryResolver, error) {
	parentResolver, err := r.cachedCommit(ctx, id, commit)
	if err != nil || parentResolver == nil {
		return nil, err
	}

	// Fast-path cache check
	parentResolver.Lock()
	cr, ok := parentResolver.children[path]
	parentResolver.Unlock()
	if ok {
		return cr, nil
	}

	parentResolver.Lock()
	defer parentResolver.Unlock()

	// Check again once locked to avoid race
	if resolver, ok := parentResolver.children[path]; ok {
		return resolver, nil
	}

	// Resolve new value and store in cache
	resolver := r.resolvePath(parentResolver.resolver, path)
	parentResolver.children[path] = resolver
	return resolver, nil
}

// Repository resolves the repository with the given identifier. This method may return a nil resolver
// if the repository is not known by gitserver - this happens if there is exists still a bundle for a
// repo that has since been deleted. This method must be called only when constructing a resolver to
// populate the cache.
func (r *CachedLocationResolver) resolveRepository(ctx context.Context, id api.RepoID) (*RepositoryResolver, error) {
	repo, err := backend.NewRepos(r.logger, r.db, r.gitserverClient).Get(ctx, id)
	if err != nil {
		if errcode.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	return NewRepositoryResolver(r.db, repo), nil
}

// Commit resolves the git commit with the given repository resolver and commit hash. This method may
// return a nil resolver if the commit is not known by gitserver. This method must be called only when
// constructing a resolver to populate the cache.
func (r *CachedLocationResolver) resolveCommit(ctx context.Context, repositoryResolver *RepositoryResolver, commit string) (*GitCommitResolver, error) {
	repo, err := repositoryResolver.Type(ctx)
	if err != nil {
		return nil, err
	}

	commitID, err := r.gitserverClient.ResolveRevision(ctx, repo.Name, commit, gitserver.ResolveRevisionOptions{NoEnsureRevision: true})
	if err != nil {
		if errors.HasType(err, &gitdomain.RevisionNotFoundError{}) {
			return nil, nil
		}
		return nil, err
	}

	return repositoryResolver.commitFromID(&resolverstubs.RepositoryCommitArgs{Rev: commit}, commitID)
}

// Path resolves the git tree entry with the given commit resolver and relative path. This method must be
// called only when constructing a resolver to populate the cache.
func (r *CachedLocationResolver) resolvePath(commitResolver *GitCommitResolver, path string) *GitTreeEntryResolver {
	return NewGitTreeEntryResolver(r.db, commitResolver, CreateFileInfo(path, false))
}
