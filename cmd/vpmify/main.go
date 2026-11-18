// Command vpmify converts UPM packages into VPM packages on the user's
// machine and registers them as ALCOM user packages.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/kaikoga/vpmify/internal/ojson"
	"github.com/kaikoga/vpmify/internal/paths"
)

// version is set at release builds with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: vpmify <command> [--json] [arguments]

commands:
  add <recipe.json|URL>...     add recipes and read their version lists
  update [<id>...]             read the version lists from upstream again;
                               converts nothing
  versions [--all] [--available] [--unity <version>] <id>
                               show the stable versions in a recipe's version
                               list: --all with prereleases, --available only
                               those available to ALCOM, --unity only those a
                               project on that Unity can use
  pull [--all] [--refetch] [<id>[@<version>]...]
                               convert versions from the version lists and
                               register them with ALCOM, which makes them
                               available: the newest for each Unity, every
                               version with --all, and each <id>@<version> named;
                               --refetch also downloads and builds every
                               available version again
  list                         show available versions
  remove <id> [<version>]      unregister and delete a version, or the whole recipe
  doctor [--fix]               find disagreements among recipes, the store and
                               ALCOM's settings; --fix fixes what it can
  clean [--yes]                show what clean deletes; --yes unregisters and deletes
                               every available version and the downloads, keeping
                               the recipes
  version                      print the version

--json prints what the command did as one JSON object on stdout, with
"error" when it failed. Progress messages still go to stderr.

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

	cmd, rest, asJSON := args[0], withoutJSON(args[1:]), slices.Contains(args[1:], "--json")
	out := output{stdout: stdout, stderr: stderr, json: asJSON}
	switch cmd {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	case "version", "--version":
		if len(rest) > 0 {
			return out.usageError("version takes only --json")
		}
		if asJSON {
			return out.result(map[string]string{"version": version}, nil)
		}
		fmt.Fprintln(stdout, "vpmify", version)
		return 0
	}

	base, err := paths.DefaultBase(os.Getenv)
	if err != nil {
		return out.result(nil, err)
	}
	a := &app.App{
		Layout:      paths.Layout{Base: base},
		Client:      fetch.NewClient("vpmify/" + version),
		Log:         stderr,
		VerifyDelay: 2 * time.Second,
	}

	switch cmd {
	case "add":
		refs, _, perr := splitFlags(rest)
		if perr != nil || len(refs) == 0 {
			return out.usageError("add takes recipe files or URLs")
		}
		res, err := a.Add(ctx, refs...)
		if err == nil && !asJSON {
			fmt.Fprintln(stderr, "`vpmify versions <id>` lists the versions; `vpmify pull` makes the newest ones available")
		}
		return out.result(res, err)
	case "update":
		ids, _, perr := splitFlags(rest)
		if perr != nil {
			return out.usageError(perr.Error())
		}
		res, err := a.Update(ctx, ids)
		return out.result(res, err)
	case "pull":
		targets, flags, perr := splitFlags(rest, "--all", "--refetch")
		if perr != nil {
			return out.usageError(perr.Error())
		}
		res, err := a.Pull(ctx, targets, app.PullOptions{All: flags["--all"], Refetch: flags["--refetch"]})
		return out.result(res, err)
	case "versions":
		rest, unity, perr := takeValue(rest, "--unity")
		if perr != nil {
			return out.usageError(perr.Error())
		}
		ids, flags, perr := splitFlags(rest, "--all", "--available")
		if perr != nil || len(ids) != 1 {
			return out.usageError("versions takes one recipe id")
		}
		opt := app.VersionsOptions{All: flags["--all"], Available: flags["--available"], Unity: unity}
		list, err := a.Versions(ids[0], opt)
		if err == nil && !asJSON {
			err = printVersions(stdout, list)
		}
		return out.result(list, err)
	case "list":
		if args, _, perr := splitFlags(rest); perr != nil || len(args) > 0 {
			return out.usageError("list takes only --json")
		}
		res, err := a.List()
		if err == nil && !asJSON {
			err = printList(stdout, stderr, res)
		}
		return out.result(res, err)
	case "remove":
		args, _, perr := splitFlags(rest)
		if perr != nil || len(args) < 1 || len(args) > 2 {
			return out.usageError("remove takes a recipe id and an optional version")
		}
		v := ""
		if len(args) == 2 {
			v = args[1]
		}
		res, err := a.Remove(ctx, args[0], v)
		return out.result(res, err)
	case "clean":
		args, flags, perr := splitFlags(rest, "--yes", "-y")
		if perr != nil || len(args) > 0 {
			return out.usageError("clean takes only --yes")
		}
		yes := flags["--yes"] || flags["-y"]
		entries, err := a.Clean(yes)
		if !asJSON {
			if perr := printClean(stdout, entries, yes, err == nil); perr != nil && err == nil {
				err = perr
			}
		}
		return out.result(cleanResult{Applied: yes && err == nil, Entries: nonNil(entries)}, err)
	case "doctor":
		args, flags, perr := splitFlags(rest, "--fix")
		if perr != nil || len(args) > 0 {
			return out.usageError("doctor takes only --fix")
		}
		problems, err := a.Doctor(flags["--fix"])
		ok := problemsLeft(problems) == 0
		if !asJSON {
			printDoctor(stdout, problems, err == nil)
		}
		if code := out.result(doctorResult{Problems: nonNil(problems), OK: ok && err == nil}, err); code != 0 {
			return code
		}
		if !ok {
			return 1
		}
		return 0
	default:
		return out.usageError(fmt.Sprintf("unknown command %q", cmd))
	}
}

// output ends a command: with --json it prints the command's result as one
// JSON object on stdout, so that a caller always has one to read.
type output struct {
	stdout, stderr io.Writer
	json           bool
}

// result prints res when the output is JSON, and err in either case, and
// returns the exit code. A failed command's result has "error" besides what
// it did; one that did nothing has only "error".
func (o output) result(res any, err error) int {
	if err != nil {
		fmt.Fprintln(o.stderr, "vpmify:", err)
	}
	if o.json {
		if werr := writeJSON(o.stdout, res, err); werr != nil {
			fmt.Fprintln(o.stderr, "vpmify:", werr)
			return 1
		}
	}
	if err != nil {
		return 1
	}
	return 0
}

func (o output) usageError(msg string) int {
	fmt.Fprintln(o.stderr, "vpmify:", msg)
	fmt.Fprint(o.stderr, usage)
	if o.json {
		_ = writeJSON(o.stdout, nil, errors.New(msg))
	}
	return 2
}

func writeJSON(w io.Writer, res any, err error) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(res); err != nil {
		return err
	}
	obj := ojson.New()
	if data := bytes.TrimSpace(buf.Bytes()); string(data) != "null" {
		var perr error
		if obj, perr = ojson.Parse(data); perr != nil {
			return perr
		}
	}
	if err != nil {
		if serr := obj.SetValue("error", err.Error()); serr != nil {
			return serr
		}
	}
	data, ferr := obj.Format("  ", "\n", true)
	if ferr != nil {
		return ferr
	}
	_, werr := w.Write(data)
	return werr
}

// withoutJSON returns args without "--json", which every command takes.
func withoutJSON(args []string) []string {
	return slices.DeleteFunc(slices.Clone(args), func(s string) bool { return s == "--json" })
}

// nonNil makes an empty list print as [] rather than null.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// cleanResult is what `clean --json` prints. Applied is false when clean
// only showed the versions it would delete.
type cleanResult struct {
	Applied bool        `json:"applied"`
	Entries []app.Entry `json:"entries"`
}

// doctorResult is what `doctor --json` prints. OK is true when no problem
// is left.
type doctorResult struct {
	Problems []app.Problem `json:"problems"`
	OK       bool          `json:"ok"`
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

// takeValue takes a flag with a value ("--unity 2022.3" or
// "--unity=2022.3") out of args, wherever it is.
func takeValue(args []string, name string) (rest []string, value string, err error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if v, ok := strings.CutPrefix(arg, name+"="); ok {
			value = v
			continue
		}
		if arg == name {
			if i+1 >= len(args) {
				return nil, "", fmt.Errorf("%s needs a value", name)
			}
			i++
			value = args[i]
			continue
		}
		rest = append(rest, arg)
	}
	return rest, value, nil
}

// printVersions prints the versions in a recipe's version list, newest
// first.
func printVersions(stdout io.Writer, list *app.VersionList) error {
	fmt.Fprintf(stdout, "%s (%s), version list of %s\n", list.Recipe, list.Package, list.Updated.Local().Format("2006-01-02 15:04"))
	if len(list.Versions) == 0 {
		fmt.Fprintln(stdout, "no version to show")
		return nil
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "VERSION\tUNITY\tFROM\tNEWEST\tAVAILABLE")
	for _, v := range list.Versions {
		newest, available := "", ""
		if v.Newest {
			newest = "yes"
		}
		if v.Available {
			available = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", v.Version, v.Unity, v.Origin, newest, available)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "`vpmify pull` makes the NEWEST ones available; `vpmify pull %s@<version>` adds another.\n", list.Recipe)
	return nil
}

// printClean prints the versions clean deletes, so the user can name the
// ones to make available again, and what to do next when clean succeeded.
func printClean(stdout io.Writer, entries []app.Entry, yes, succeeded bool) error {
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
	if !succeeded {
		return nil
	}
	if !yes {
		fmt.Fprintf(stdout, "clean deletes these %d versions and the downloads; the recipes stay.\n", len(entries))
		fmt.Fprintln(stdout, "a project that locks a version not made available again cannot resolve it.")
		fmt.Fprintln(stdout, "run `vpmify clean --yes` to delete them.")
		return nil
	}
	fmt.Fprintf(stdout, "deleted %d versions and the downloads.\n", len(entries))
	fmt.Fprintln(stdout, "run `vpmify pull` to make the newest versions available again, and")
	fmt.Fprintln(stdout, "`vpmify pull <id>@<version>` for each other version a project needs.")
	return nil
}

// problemsLeft counts the problems doctor did not fix.
func problemsLeft(problems []app.Problem) int {
	left := 0
	for _, p := range problems {
		if !p.Fixed {
			left++
		}
	}
	return left
}

// printDoctor prints the problems, or that there are none when doctor
// succeeded.
func printDoctor(stdout io.Writer, problems []app.Problem, succeeded bool) {
	if len(problems) == 0 {
		if succeeded {
			fmt.Fprintln(stdout, "no problems found")
		}
		return
	}
	for _, p := range problems {
		mark := "!"
		switch {
		case p.Fixed:
			mark = "fixed:"
		case p.Fix != "":
			mark = "fixable:"
		}
		fmt.Fprintln(stdout, mark, p.Message)
		if p.Fix != "" && !p.Fixed {
			fmt.Fprintln(stdout, "    --fix would", p.Fix)
		}
	}
}

func printList(stdout, stderr io.Writer, res *app.ListResult) error {
	if res.SettingsError != "" {
		fmt.Fprintln(stderr, "vpmify: cannot tell what is registered:", res.SettingsError)
	}
	if len(res.Entries) == 0 {
		fmt.Fprintln(stdout, "no version is available")
		return nil
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "RECIPE\tPACKAGE\tVERSION\tUNITY\tREGISTERED")
	for _, e := range res.Entries {
		reg := "no"
		if e.Registered {
			reg = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", e.Recipe, e.Package, e.Version, e.Unity, reg)
	}
	return tw.Flush()
}
