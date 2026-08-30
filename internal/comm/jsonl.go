package comm

import (
	"time"
)

// jsonlStore is the JSONL Store at <root>/.comm.jsonl. Writes run under
// alloc.WithLock(root, …) — the SAME <root>/.lock the registry uses; one
// lock for all root state — as load → prune → append → atomic save (temp
// file in root, fsync, rename: the alloc.Save shape, see
// internal/alloc/registry.go). Reads take no lock: the rename guarantees a
// reader never observes a half-written file.
type jsonlStore struct {
	root string
}

// Open returns the JSONL store for the board at <root>/.comm.jsonl.
func Open(root string) Store {
	return &jsonlStore{root: root}
}

func (s *jsonlStore) Append(from, text string, now time.Time) (Record, error) {
	panic("comm: Append not implemented — this one is cat's")
}

func (s *jsonlStore) List(warn func(string)) ([]Record, error) {
	panic("comm: List not implemented — this one is cat's")
}

func (s *jsonlStore) Prune(now time.Time) error {
	panic("comm: Prune not implemented — this one is cat's")
}
