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

recipes and their version lists:
  recipe add <recipe.json|URL>...
                               add recipes and read their version lists
  recipe update [<id>...]      read the recipes added from URLs again from
                               them, and the version lists from upstream;
                               converts nothing
  recipe versions [--all] [--unity <version>] <id>[/<package>]
                               show the stable versions in the version lists of
                               a recipe's packages, or of one package: --all
                               with prereleases, --unity only those a project
                               on that Unity can use
  recipe list                  show the recipes
  recipe remove <id>           unregister and delete the recipe's available
                               versions, and delete the recipe and its version
                               lists

available versions, which ALCOM lists as user packages:
  package list [<id>...]       show the available versions, of every recipe or
                               of the ones named
  package pull [--all] [--prerelease] [--refetch] [<id>[/<package>][@<version>]...]
                               convert versions from the version lists and
                               register them with ALCOM, which makes them
                               available: the newest stable version of each
                               package (of the one named after "/"; a package
                               the recipe marks onDemand only when named),
                               every stable version with --all, and each
                               @<version> named (of every package that has it,
                               without "/"); --prerelease lets the newest, or
                               every version with --all, be a prerelease too;
                               --refetch also downloads and builds every
                               available version again
  package remove <id>[/<package>]@<version>
  package remove --all <id>[/<package>]
                               unregister and delete a version, or every
                               version, of a recipe's packages or of one
                               package; the recipe and its version lists stay,
                               so package pull can make them available again

other commands:
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
	asJSON := slices.Contains(args, "--json")
	args = slices.DeleteFunc(slices.Clone(args), func(s string) bool { return s == "--json" })
	out := output{stdout: stdout, stderr: stderr, json: asJSON}
	if len(args) == 0 {
		if asJSON {
			return out.usageError("no command")
		}
		fmt.Fprint(stderr, usage)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "help", "-h", "--help":
		if asJSON {
			return out.result(map[string]string{"usage": usage}, nil)
		}
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
	case "recipe":
		return runRecipe(ctx, a, out, rest)
	case "package":
		return runPackage(ctx, a, out, rest)
	case "clean":
		args, flags, perr := splitFlags(rest, "--yes", "-y")
		if perr != nil || len(args) > 0 {
			return out.usageError("clean takes only --yes")
		}
		yes := flags["--yes"] || flags["-y"]
		entries, changed, err := a.Clean(yes)
		if !asJSON {
			if perr := printClean(stdout, entries, yes, err == nil); perr != nil && err == nil {
				err = perr
			}
		}
		return out.result(cleanResult{Applied: changed, Entries: nonNil(entries)}, err)
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
		if moved, ok := movedCommands[cmd]; ok {
			return out.usageError(fmt.Sprintf("unknown command %q; it is now %s", cmd, moved))
		}
		return out.usageError(fmt.Sprintf("unknown command %q", cmd))
	}
}

// movedCommands are the commands that took no namespace before the commands
// were split into recipe and package, with where they went, so that a user
// of the old form learns the new one.
var movedCommands = map[string]string{
	"add":      "`vpmify recipe add`",
	"update":   "`vpmify recipe update`",
	"versions": "`vpmify recipe versions`",
	"pull":     "`vpmify package pull`",
	"list":     "`vpmify package list`",
	"remove":   "`vpmify recipe remove` for a recipe, or `vpmify package remove` for versions",
}

// runRecipe runs the commands on recipes and their version lists.
func runRecipe(ctx context.Context, a *app.App, out output, args []string) int {
	if len(args) == 0 {
		return out.usageError("recipe needs a command: add, update, versions, list or remove")
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "add":
		// Every argument is a recipe, even one that starts with "-".
		if len(rest) == 0 {
			return out.usageError("recipe add takes recipe files or URLs")
		}
		res, err := a.Add(ctx, rest...)
		if err == nil && !out.json {
			fmt.Fprintln(out.stderr, "`vpmify recipe versions <id>` lists the versions; `vpmify package pull` makes the newest ones available")
		}
		return out.result(res, err)
	case "update":
		ids, _, perr := splitFlags(rest)
		if perr != nil {
			return out.usageError(perr.Error())
		}
		res, err := a.Update(ctx, ids)
		return out.result(res, err)
	case "versions":
		rest, unity, perr := takeValue(rest, "--unity")
		if perr != nil {
			return out.usageError(perr.Error())
		}
		ids, flags, perr := splitFlags(rest, "--all")
		if perr != nil || len(ids) != 1 {
			return out.usageError("recipe versions takes one recipe id, or <id>/<package>")
		}
		list, err := a.Versions(ids[0], app.VersionsOptions{All: flags["--all"], Unity: unity})
		if list != nil && !out.json {
			if perr := printVersions(out.stdout, list); perr != nil && err == nil {
				err = perr
			}
		}
		return out.result(list, err)
	case "list":
		if args, _, perr := splitFlags(rest); perr != nil || len(args) > 0 {
			return out.usageError("recipe list takes only --json")
		}
		recipes, err := a.Recipes()
		if err == nil && !out.json {
			err = printRecipes(out.stdout, recipes)
		}
		return out.result(recipeListResult{Recipes: nonNil(recipes)}, err)
	case "remove":
		// Removing without asking, as vrc-get removes a repository; the GUI
		// asks.
		ids, _, perr := splitFlags(rest)
		if perr != nil || len(ids) != 1 {
			return out.usageError("recipe remove takes one recipe id")
		}
		res, err := a.RemoveRecipe(ids[0], true)
		if res != nil && !out.json {
			if perr := printRemoveRecipe(out.stdout, ids[0], res, err == nil); perr != nil && err == nil {
				err = perr
			}
		}
		return out.result(res, err)
	default:
		return out.usageError(fmt.Sprintf("unknown command \"recipe %s\"", cmd))
	}
}

// runPackage runs the commands on available versions.
func runPackage(ctx context.Context, a *app.App, out output, args []string) int {
	if len(args) == 0 {
		return out.usageError("package needs a command: list, pull or remove")
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "list":
		ids, _, perr := splitFlags(rest)
		if perr != nil {
			return out.usageError(perr.Error())
		}
		res, err := a.List(ids...)
		if err == nil && !out.json {
			err = printList(out.stdout, out.stderr, res)
		}
		return out.result(res, err)
	case "pull":
		targets, flags, perr := splitFlags(rest, "--all", "--prerelease", "--refetch")
		if perr != nil {
			return out.usageError(perr.Error())
		}
		res, err := a.Pull(ctx, targets, app.PullOptions{All: flags["--all"], Prerelease: flags["--prerelease"], Refetch: flags["--refetch"]})
		return out.result(res, err)
	case "remove":
		targets, flags, perr := splitFlags(rest, "--all")
		if perr != nil || len(targets) != 1 {
			return out.usageError("package remove takes one <id>[/<package>]@<version>, or --all and one <id>[/<package>]")
		}
		// A version or --all is always named, so that removing every version
		// is never what a short command does by chance.
		ref, version, named := strings.Cut(targets[0], "@")
		switch {
		case flags["--all"] && named:
			return out.usageError("package remove takes either @<version> or --all, not both")
		case !flags["--all"] && (!named || version == ""):
			return out.usageError("package remove needs @<version>, or --all to remove every version")
		}
		res, err := a.RemoveVersions(ref, version)
		return out.result(res, err)
	default:
		return out.usageError(fmt.Sprintf("unknown command \"package %s\"", cmd))
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

// nonNil makes an empty list print as [] rather than null.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// cleanResult is what `clean --json` prints. Applied is true when clean
// changed anything, even when it then failed partway, and false when it
// only showed the versions it would delete.
type cleanResult struct {
	Applied bool        `json:"applied"`
	Entries []app.Entry `json:"entries"`
}

// recipeListResult is what `recipe list --json` prints.
type recipeListResult struct {
	Recipes []app.RecipeInfo `json:"recipes"`
}

// doctorResult is what `doctor --json` prints. OK is true when no problem
// is left.
type doctorResult struct {
	Problems []app.Problem `json:"problems"`
	OK       bool          `json:"ok"`
}

// splitFlags takes the boolean flags names out of args, wherever they are,
// so that `package pull kit --refetch` works as well as
// `package pull --refetch kit`.
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

// printVersions prints the versions in the version lists of a recipe's
// packages, newest first.
func printVersions(stdout io.Writer, list *app.VersionList) error {
	for i, p := range list.Packages {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		fmt.Fprintf(stdout, "%s/%s (%s), version list of %s\n", list.Recipe, p.Package, p.Source, p.Updated.Local().Format("2006-01-02 15:04"))
		if len(p.Versions) == 0 {
			fmt.Fprintln(stdout, "no version to show")
			continue
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "VERSION\tUNITY\tFROM\tAVAILABLE")
		var unusable []app.VersionInfo
		for _, v := range p.Versions {
			available := ""
			if v.Unusable != "" {
				// "-": it cannot be made available; why follows the table.
				available = "-"
				unusable = append(unusable, v)
			}
			if v.Available {
				available = "yes"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", v.Version, v.Unity, v.Origin, available)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		for _, v := range unusable {
			fmt.Fprintf(stdout, "%s cannot be converted: %s\n", v.Version, v.Unusable)
		}
	}
	fmt.Fprintf(stdout, "\n`vpmify package pull` makes the newest version of each package available; `vpmify package pull %s/<package>@<version>` adds another.\n", list.Recipe)
	return nil
}

// printRecipes prints the user's recipes, and why one cannot be used.
func printRecipes(stdout io.Writer, recipes []app.RecipeInfo) error {
	if len(recipes) == 0 {
		fmt.Fprintln(stdout, "no recipes; add one with `vpmify recipe add <recipe.json>`")
		return nil
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "RECIPE\tPACKAGES\tFROM")
	for _, r := range recipes {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", r.ID, strings.Join(r.Packages, " "), r.Origin)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, r := range recipes {
		if r.Problem != "" {
			fmt.Fprintf(stdout, "%s cannot be used: %s\n", r.ID, r.Problem)
		}
	}
	return nil
}

// printRemoveRecipe prints the versions removing recipe id deleted, and
// what that means for projects.
func printRemoveRecipe(stdout io.Writer, id string, res *app.RemoveResult, succeeded bool) error {
	if err := printEntries(stdout, res.Removed); err != nil {
		return err
	}
	if !succeeded {
		return nil
	}
	fmt.Fprintf(stdout, "removed %s and its %d versions.\n", id, len(res.Removed))
	if len(res.Removed) > 0 {
		fmt.Fprintln(stdout, "projects keep the copies they have, but ALCOM can no longer update or install these again.")
	}
	fmt.Fprintln(stdout, "`vpmify recipe add` takes the recipe back.")
	return nil
}

// printEntries prints available versions as a table, or nothing when there
// are none.
func printEntries(stdout io.Writer, entries []app.Entry) error {
	if len(entries) == 0 {
		return nil
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "RECIPE\tPACKAGE\tVERSION\tUNITY")
	for _, e := range entries {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Recipe, e.Package, e.Version, e.Unity)
	}
	return tw.Flush()
}

// printClean prints the versions clean deletes, so the user can name the
// ones to make available again, and what to do next when clean succeeded.
func printClean(stdout io.Writer, entries []app.Entry, yes, succeeded bool) error {
	if err := printEntries(stdout, entries); err != nil {
		return err
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
	fmt.Fprintln(stdout, "run `vpmify package pull` to make the newest versions available again, and")
	fmt.Fprintln(stdout, "`vpmify package pull <id>/<package>@<version>` for each other version a project needs.")
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
