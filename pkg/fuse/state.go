package ghfuse

import (
	"github.com/berthadev/ghfs/pkg/cache"
	"github.com/berthadev/ghfs/pkg/fs"
	"github.com/berthadev/ghfs/pkg/github"
	"github.com/berthadev/ghfs/pkg/store"
)

// mountState holds shared state passed to all FUSE nodes.
type mountState struct {
	ghfs       *fs.FS
	store      store.Index
	client     github.ContentAPI
	cache      *cache.Cache
	tree       *DirTree
	passphrase string
}

// NewMountState creates a mountState with the given dependencies.
func NewMountState(
	ghfs *fs.FS,
	store store.Index,
	client github.ContentAPI,
	cache *cache.Cache,
	tree *DirTree,
	passphrase string,
) *mountState {
	return &mountState{
		ghfs:       ghfs,
		store:      store,
		client:     client,
		cache:      cache,
		tree:       tree,
		passphrase: passphrase,
	}
}
