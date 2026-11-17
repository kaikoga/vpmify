// Command vpmify converts UPM packages into VPM packages on the user's
// machine and registers them as ALCOM user packages.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"text/tabwriter"
	"time"

	"github.com/kaikoga/vpmify/internal/app"
	"github.com/kaikoga/vpmify/internal/fetch"
	"github.com/kaikoga/vpmify/internal/paths"
)

// version is set at release builds with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: vpmify <command> [arguments]

commands:
  add <recipe.json|URL>...     add recipes and sync
  sync [<id>[@<version>]...]   place selected versions and register them with ALCOM
  list [--json]                show placed versions
  remove <id> [<version>]      unregister and delete a version, or the whole recipe
  version                      print the version

Data lives next to ALCOM's folder: $XDG_DATA_HOME/Vpmify (default
~/.local/share/Vpmify), or %LOCALAPPDATA%\Vpmify on Windows.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	case "version", "--version":
		fmt.Fprintln(stdout, "vpmify", version)
		return 0
	}

	base, err := paths.DefaultBase(os.Getenv)
	if err != nil {
		fmt.Fprintln(stderr, "vpmify:", err)
		return 1
	}
	a := &app.App{
		Layout:      paths.Layout{Base: base},
		Client:      fetch.NewClient("vpmify/" + version),
		Log:         stderr,
		VerifyDelay: 2 * time.Second,
	}

	switch cmd {
	case "add":
		if len(rest) == 0 {
			return usageError(stderr, "add takes recipe files or URLs")
		}
		err = a.Add(ctx, rest...)
	case "sync":
		err = a.Sync(ctx, rest)
	case "list":
		fs := flag.NewFlagSet("list", flag.ContinueOnError)
		fs.SetOutput(stderr)
		asJSON := fs.Bool("json", false, "print JSON")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		err = list(a, stdout, stderr, *asJSON)
	case "remove":
		if len(rest) < 1 || len(rest) > 2 {
			return usageError(stderr, "remove takes a recipe id and an optional version")
		}
		v := ""
		if len(rest) == 2 {
			v = rest[1]
		}
		err = a.Remove(ctx, rest[0], v)
	default:
		return usageError(stderr, fmt.Sprintf("unknown command %q", cmd))
	}
	if err != nil {
		fmt.Fprintln(stderr, "vpmify:", err)
		return 1
	}
	return 0
}

func usageError(stderr io.Writer, msg string) int {
	fmt.Fprintln(stderr, "vpmify:", msg)
	fmt.Fprint(stderr, usage)
	return 2
}

func list(a *app.App, stdout, stderr io.Writer, asJSON bool) error {
	entries, settingsErr, err := a.List()
	if err != nil {
		return err
	}
	if settingsErr != nil {
		fmt.Fprintln(stderr, "vpmify: cannot tell what is registered:", settingsErr)
	}
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	}
	if len(entries) == 0 {
		fmt.Fprintln(stdout, "nothing placed")
		return nil
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "RECIPE\tPACKAGE\tVERSION\tUNITY\tREGISTERED")
	for _, e := range entries {
		reg := "no"
		if e.Registered {
			reg = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", e.Recipe, e.Package, e.Version, e.Unity, reg)
	}
	return tw.Flush()
}
