// Package paths decides where Vpmify keeps its data and where ALCOM keeps
// settings.json. Vpmify uses the same base folder as ALCOM
// (vrc-get-vpm/src/io/tokio.rs, get_local_config_folder): $XDG_DATA_HOME or
// ~/.local/share outside Windows, and LocalAppData on Windows.
package paths

import (
	"errors"
	"path/filepath"
	"runtime"
)

// Layout holds the folders Vpmify reads and writes.
type Layout struct {
	// Base is the folder that holds both ALCOM's and Vpmify's folders.
	Base string
}

// DefaultBase returns the base folder for the running OS.
func DefaultBase(getenv func(string) string) (string, error) {
	return defaultBase(runtime.GOOS, getenv)
}

func defaultBase(goos string, getenv func(string) string) (string, error) {
	if goos == "windows" {
		// ALCOM asks the shell for the LocalAppData known folder; the
		// environment variable points to the same folder for a normal user.
		if v := getenv("LOCALAPPDATA"); v != "" {
			return v, nil
		}
		return "", errors.New("LOCALAPPDATA is not set")
	}
	if v := getenv("XDG_DATA_HOME"); v != "" {
		return v, nil
	}
	if v := getenv("HOME"); v != "" {
		return filepath.Join(v, ".local", "share"), nil
	}
	return "", errors.New("neither XDG_DATA_HOME nor HOME is set")
}

// ALCOMSettings is ALCOM's (and VCC's) settings.json.
func (l Layout) ALCOMSettings() string {
	return filepath.Join(l.Base, "VRChatCreatorCompanion", "settings.json")
}

// Home is Vpmify's own folder.
func (l Layout) Home() string { return filepath.Join(l.Base, "Vpmify") }

// Packages is the store of converted packages.
func (l Layout) Packages() string { return filepath.Join(l.Home(), "packages") }

// PackageDir is the folder of one version of a package in the store.
func (l Layout) PackageDir(name, version string) string {
	return filepath.Join(l.Packages(), name, version)
}

// Cache holds downloaded archives.
func (l Layout) Cache() string { return filepath.Join(l.Home(), "cache") }

// Recipes holds the user's recipes.
func (l Layout) Recipes() string { return filepath.Join(l.Home(), "recipes") }

// Tmp holds work folders. It is on the same volume as the store, so that a
// finished folder can be renamed into place.
func (l Layout) Tmp() string { return filepath.Join(l.Home(), "tmp") }

// State is the record of what Vpmify has placed and registered.
func (l Layout) State() string { return filepath.Join(l.Home(), "state.json") }

// Lock is the machine-wide lock file.
func (l Layout) Lock() string { return filepath.Join(l.Home(), "vpmify.lock") }
