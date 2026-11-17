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
	"slices"
	"strings"
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
  add <recipe.json|URL>...     add recipes and read their version lists
  update [<id>...]             read the version lists from upstream again;
                               places nothing
  versions [--json] <id>       show the versions a recipe can place, from its
                               version list
  pull [--all] [--refetch] [<id>[@<version>]...]
                               place versions from the version lists and register
                               them with ALCOM: the newest for each Unity, every
                               version with --all, and each <id>@<version> named;
                               --refetch also downloads and builds every placed
                               version again
  list [--json]                show placed versions
  remove <id> [<version>]      unregister and delete a version, or the whole recipe
  doctor [--fix]               find disagreements among recipes, the store and
                               ALCOM's settings; --fix fixes what it can
  clean [--yes]                show what clean deletes; --yes unregisters and deletes
                               every placed version and the downloads, keeping the
                               recipes
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
	case "update":
		ids, _, perr := splitFlags(rest)
		if perr != nil {
			return usageError(stderr, perr.Error())
		}
		err = a.Update(ctx, ids)
	case "pull":
		targets, flags, perr := splitFlags(rest, "--all", "--refetch")
		if perr != nil {
			return usageError(stderr, perr.Error())
		}
		err = a.Pull(ctx, targets, app.PullOptions{All: flags["--all"], Refetch: flags["--refetch"]})
	case "versions":
		ids, flags, perr := splitFlags(rest, "--json")
		if perr != nil || len(ids) != 1 {
			return usageError(stderr, "versions takes one recipe id and an optional --json")
		}
		err = versions(a, stdout, ids[0], flags["--json"])
	case "sync":
		return usageError(stderr, "sync is now `update` (read the version lists) and `pull` (place versions)")
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
	case "clean":
		args, flags, perr := splitFlags(rest, "--yes", "-y")
		if perr != nil || len(args) > 0 {
			return usageError(stderr, "clean takes only --yes")
		}
		err = clean(a, stdout, flags["--yes"] || flags["-y"])
	case "doctor":
		args, flags, perr := splitFlags(rest, "--fix")
		if perr != nil || len(args) > 0 {
			return usageError(stderr, "doctor takes only --fix")
		}
		var ok bool
		ok, err = doctor(a, stdout, flags["--fix"])
		if err == nil && !ok {
			return 1
		}
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

// splitFlags takes the boolean flags names out of args, wherever they are,
// so that `pull kit --refetch` works as well as `pull --refetch kit`.
func splitFlags(args []string, names ...string) (rest []string, set map[string]bool, err error) {
	set = map[string]bool{}
	for _, arg := range args {
		if slices.Contains(names, arg) {
			set[arg] = true
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return nil, nil, fmt.Errorf("unknown flag %q", arg)
		}
		rest = append(rest, arg)
	}
	return rest, set, nil
}

// versions prints the versions recipe id can place, newest first.
func versions(a *app.App, stdout io.Writer, id string, asJSON bool) error {
	list, err := a.Versions(id)
	if err != nil {
		return err
	}
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(list)
	}
	fmt.Fprintf(stdout, "%s (%s), version list of %s\n", list.Recipe, list.Package, list.Updated.Local().Format("2006-01-02 15:04"))
	if len(list.Versions) == 0 {
		fmt.Fprintln(stdout, "no version matches the recipe")
		return nil
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "VERSION\tUNITY\tFROM\tNEWEST\tPLACED")
	for _, v := range list.Versions {
		newest, placed := "", ""
		if v.Newest {
			newest = "yes"
		}
		if v.Placed {
			placed = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", v.Version, v.Unity, v.Origin, newest, placed)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "`vpmify pull` places the NEWEST ones; `vpmify pull %s@<version>` places another.\n", list.Recipe)
	return nil
}

// clean prints the versions clean deletes, so the user can name the ones to
// place again, and deletes them when yes is true.
func clean(a *app.App, stdout io.Writer, yes bool) error {
	entries, err := a.Clean(yes)
	if len(entries) > 0 {
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "RECIPE\tPACKAGE\tVERSION\tUNITY")
		for _, e := range entries {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Recipe, e.Package, e.Version, e.Unity)
		}
		if ferr := tw.Flush(); ferr != nil {
			return ferr
		}
	}
	if err != nil {
		return err
	}
	if !yes {
		fmt.Fprintf(stdout, "clean deletes these %d versions and the downloads; the recipes stay.\n", len(entries))
		fmt.Fprintln(stdout, "a project that locks a version not placed again cannot resolve it.")
		fmt.Fprintln(stdout, "run `vpmify clean --yes` to delete them.")
		return nil
	}
	fmt.Fprintf(stdout, "deleted %d versions and the downloads.\n", len(entries))
	fmt.Fprintln(stdout, "run `vpmify pull` to place the newest versions again, and")
	fmt.Fprintln(stdout, "`vpmify pull <id>@<version>` for each other version a project needs.")
	return nil
}

// doctor prints the problems and reports whether none is left.
func doctor(a *app.App, stdout io.Writer, fix bool) (bool, error) {
	problems, err := a.Doctor(fix)
	if len(problems) == 0 {
		if err == nil {
			fmt.Fprintln(stdout, "no problems found")
		}
		return err == nil, err
	}
	left := 0
	for _, p := range problems {
		mark := "!"
		switch {
		case p.Fixed:
			mark = "fixed:"
		case p.Fix != "":
			left++
			mark = "fixable:"
		default:
			left++
		}
		fmt.Fprintln(stdout, mark, p.Message)
		if p.Fix != "" && !p.Fixed {
			fmt.Fprintln(stdout, "    --fix would", p.Fix)
		}
	}
	return left == 0, err
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
