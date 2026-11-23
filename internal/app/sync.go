package app

import (
	"github.com/kaikoga/vpmify/internal/state"
)

// SyncResult is what Sync did.
type SyncResult struct {
	// Registered are the folders Sync registered with ALCOM.
	Registered []string `json:"registered"`
}

// Sync registers with ALCOM every version Vpmify keeps a record of whose
// folder is in the store, for when ALCOM's user packages lost some, as
// when ALCOM wrote settings.json back over a change Vpmify had just made,
// or when the user unregistered a version in ALCOM. It only registers:
// what it would unregister, a registration whose folder is gone, ALCOM
// cannot read anyway, and doctor --fix forgets it. Folders outside the
// store, which the user may have added with the same packages, are never
// touched.
func (a *App) Sync() (*SyncResult, error) {
	res := &SyncResult{Registered: []string{}}
	err := a.withLock(func() error {
		st, err := state.Load(a.Layout.State())
		if err != nil {
			return err
		}
		var want []string
		for _, id := range state.SortedKeys(st.Recipes) {
			rs := st.Recipes[id]
			for _, name := range state.SortedKeys(rs.Packages) {
				for _, v := range state.SortedKeys(rs.Packages[name].Versions) {
					if dir, ok := a.recordDir(id, name, v); ok && isDir(dir) {
						want = append(want, dir)
					}
				}
			}
		}
		change, err := a.updateRegistration(want, nil)
		res.Registered = append(res.Registered, change.Added...)
		if err == nil && len(change.Added) == 0 {
			a.logf("every version Vpmify keeps is registered")
		}
		return err
	})
	return res, err
}
