import {Browser, Events} from "@wailsio/runtime";
import {type PendingAdd, VpmifyService, type VersionsResult} from "../bindings/github.com/kaikoga/vpmify/gui";
import type {Entry, PackageVersions, RecipeInfo} from "../bindings/github.com/kaikoga/vpmify/internal/app";
import {Busy, Log, Pending} from "../bindings/events";
import {applyStatic, currentLang, type Key, type Lang, setLang, t} from "./i18n";

function byId<T extends HTMLElement>(id: string): T {
    return document.getElementById(id)! as T;
}

const errorElement = byId<HTMLParagraphElement>("error");
const spinnerElement = byId<HTMLSpanElement>("spinner");
const cancelElement = byId<HTMLButtonElement>("cancel");
const languageElement = byId<HTMLSelectElement>("language");
const recipesElement = byId<HTMLUListElement>("recipes");
const noRecipesElement = byId<HTMLParagraphElement>("no-recipes");
const recipeElement = byId<HTMLDivElement>("recipe");
const recipeLabel = byId<HTMLDivElement>("recipe-label");
const recipeProblem = byId<HTMLParagraphElement>("recipe-problem");
const recipeOrigin = byId<HTMLElement>("recipe-origin");
const recipePackages = byId<HTMLElement>("recipe-packages");
const recipeAvailable = byId<HTMLElement>("recipe-available");
const prereleasesElement = byId<HTMLInputElement>("prereleases");
const pullNewestElement = byId<HTMLButtonElement>("pull-newest");
const versionsError = byId<HTMLParagraphElement>("versions-error");
const packagesElement = byId<HTMLDivElement>("packages");
const noVersionsElement = byId<HTMLParagraphElement>("no-versions");
const entriesElement = byId<HTMLTableSectionElement>("entries");
const emptyElement = byId<HTMLParagraphElement>("empty");
const availableError = byId<HTMLParagraphElement>("available-error");
const logFooter = byId<HTMLElement>("log-footer");
const logElement = byId<HTMLPreElement>("log");
const logToggle = byId<HTMLButtonElement>("log-toggle");
const confirmDialog = byId<HTMLDialogElement>("confirm-dialog");
const pendingDialog = byId<HTMLDialogElement>("pending-dialog");
const aboutSource = byId<HTMLAnchorElement>("about-source");

// selected is the recipe chosen in the list on the left.
let selected: string | null = null;

function el<K extends keyof HTMLElementTagNameMap>(tag: K, className = "", text?: string): HTMLElementTagNameMap[K] {
    const e = document.createElement(tag);
    if (className) {
        e.className = className;
    }
    if (text !== undefined) {
        e.textContent = text;
    }
    return e;
}

// label shows a display name with the id below it, small and faint, as
// ALCOM shows a package; the id alone when there is no display name.
function label(id: string, displayName?: string): HTMLDivElement {
    const box = el("div", "label");
    box.append(el("div", "main", displayName || id));
    if (displayName) {
        box.append(el("div", "id", id));
    }
    return box;
}

// nameLabel is label on one line, for sentences.
function nameLabel(id: string, displayName?: string): string {
    return displayName ? `${displayName} (${id})` : id;
}

function headRow(table: HTMLTableElement, keys: (Key | null)[]) {
    const head = table.createTHead().insertRow();
    for (const k of keys) {
        head.appendChild(el("th", "", k ? t(k) : ""));
    }
}

// busy is what the last busy event said; setBusy sets it.
let busy = false;

// button makes a button. The button of an operation on the core is off
// while busy, as setBusy turns off the ones it finds.
function button(text: string, onClick: () => void, opts: { op?: boolean, className?: string } = {}): HTMLButtonElement {
    const b = el("button", opts.className ?? "", text);
    b.type = "button";
    if (opts.op) {
        b.dataset.op = "";
        b.disabled = busy;
    }
    b.addEventListener("click", onClick);
    return b;
}

// Removal is what the removal dialog asks about.
interface Removal {
    title: string;
    // before and after go above and below the table of versions.
    before: string;
    entries: Entry[];
    after: string;
    confirm: string;
    run: () => Promise<unknown>;
}

// confirmRemoval removes only when the user confirms, as removing is
// destructive.
function confirmRemoval(r: Removal) {
    // The recipes waiting to be added are asked about again once this
    // dialog closes.
    hidePending();
    confirmDialog.replaceChildren(el("h3", "", r.title), el("p", "", r.before));
    if (r.entries.length > 0) {
        const table = el("table");
        headRow(table, ["col.package", "col.version", "col.unity"]);
        const body = table.createTBody();
        for (const e of r.entries) {
            const row = body.insertRow();
            row.insertCell().append(label(e.package, e.displayName));
            row.insertCell().textContent = e.version;
            row.insertCell().textContent = e.unity ?? "";
        }
        confirmDialog.append(table);
    }
    if (r.after) {
        confirmDialog.append(el("p", "", r.after));
    }
    const actions = el("div", "actions");
    actions.append(
        button(t("remove.cancel"), closeConfirm),
        button(r.confirm, () => {
            closeConfirm();
            act(r.run);
        }, {op: true, className: "danger"}),
    );
    confirmDialog.append(actions);
    confirmDialog.showModal();
}

function closeConfirm() {
    if (confirmDialog.open) {
        confirmDialog.close();
    }
}

async function confirmRecipeRemoval(r: RecipeInfo) {
    clearError();
    let preview;
    try {
        preview = await VpmifyService.RecipeRemoval(r.id);
    } catch (err) {
        showError(err);
        return;
    }
    // Canceled before it was read.
    if (!preview) {
        return;
    }
    const entries = preview.removed ?? [];
    confirmRemoval({
        title: t("remove.recipe.title", {label: nameLabel(r.id, r.displayName)}),
        before: t(entries.length > 0 ? "remove.recipe.before" : "remove.recipe.beforeNone"),
        entries,
        after: entries.length > 0 ? t("remove.recipe.after") : "",
        confirm: t("remove.recipe.confirm"),
        run: () => VpmifyService.RemoveRecipe(r.id),
    });
}

function confirmVersionRemoval(e: Entry) {
    const params = {label: nameLabel(e.package, e.displayName), version: e.version};
    confirmRemoval({
        title: t("remove.version.title", params),
        before: t("remove.version.before", params),
        entries: [],
        after: t("remove.version.after"),
        confirm: t("remove.version.confirm"),
        run: () => VpmifyService.RemoveVersions(`${e.recipe}/${e.package}`, e.version),
    });
}

function confirmPackageRemoval(id: string, name: string, displayName: string | undefined, entries: Entry[]) {
    const params = {label: nameLabel(name, displayName)};
    confirmRemoval({
        title: t("remove.package.title", params),
        before: t("remove.package.before", params),
        entries,
        after: t("remove.package.after"),
        confirm: t("remove.package.confirm"),
        run: () => VpmifyService.RemoveVersions(`${id}/${name}`, ""),
    });
}

function showError(err: unknown) {
    errorElement.hidden = false;
    errorElement.textContent = String(err);
}

function clearError() {
    errorElement.hidden = true;
    errorElement.textContent = "";
}

// act runs one operation the user asked for, then shows what changed.
async function act(fn: () => Promise<unknown>) {
    clearError();
    try {
        await fn();
    } catch (err) {
        showError(err);
    }
    await refresh();
}

// newestChoosable says whether the newest button has a version to choose;
// showVersions sets it, and setBusy keeps the button off without one.
let newestChoosable = false;

function setBusy(value: boolean) {
    busy = value;
    spinnerElement.hidden = !busy;
    cancelElement.hidden = !busy;
    for (const b of document.querySelectorAll<HTMLButtonElement>("button[data-op]")) {
        b.disabled = busy || (b === pullNewestElement && !newestChoosable);
    }
    if (busy) {
        // The dialog would cover the cancel button; it comes back when the
        // operations end.
        hidePending();
    } else {
        showPending().catch(showError);
    }
}

function appendLog(line: string) {
    logElement.textContent += line + "\n";
    logElement.scrollTop = logElement.scrollHeight;
}

function setLogOpen(open: boolean) {
    logFooter.classList.toggle("open", open);
    logToggle.textContent = t(open ? "log.shrink" : "log.expand");
}

// hiding says that the GUI itself closes the dialog of the recipes waiting
// to be added; when the dialog closes otherwise, they are asked about again.
let hiding = false;

function hidePending() {
    if (pendingDialog.open) {
        hiding = true;
        pendingDialog.close();
    }
}

// showPending asks about the oldest recipes waiting to be added. It waits
// while the removal dialog is open, and while an operation runs, as the
// dialog would cover the cancel button.
async function showPending() {
    const pending = (await VpmifyService.Pending()) ?? [];
    const p = pending[0];
    if (!p) {
        hidePending();
        return;
    }
    if (confirmDialog.open || busy) {
        return;
    }
    renderPending(p, pending.length - 1);
    if (!pendingDialog.open) {
        pendingDialog.showModal();
    }
}

function renderPending(p: PendingAdd, more: number) {
    pendingDialog.replaceChildren(
        el("h3", "", t("pending.title")),
        el("p", "note", t("pending.from", {from: (p.from ?? []).join(", ")})),
    );
    if (p.error) {
        pendingDialog.append(el("p", "error", p.error));
    } else {
        const table = el("table");
        headRow(table, ["col.recipe", "col.package", "col.source", "col.versions", null]);
        const body = table.createTBody();
        // One row per package; the recipe and whether it is new go on its
        // first row.
        for (const r of p.recipes ?? []) {
            (r.packages ?? []).forEach((pkg, i) => {
                const row = body.insertRow();
                row.title = r.origin;
                const recipeCell = row.insertCell();
                if (i === 0) {
                    recipeCell.append(label(r.id, r.displayName));
                }
                row.insertCell().textContent = pkg.name;
                row.insertCell().textContent = pkg.source;
                row.insertCell().textContent = pkg.versions || t("pending.anyVersion");
                row.insertCell().textContent = i === 0 ? t(r.replaces ? "pending.replaces" : "pending.new") : "";
            });
        }
        pendingDialog.append(table);
    }
    if (more > 0) {
        pendingDialog.append(el("p", "note", t("pending.more", {count: more})));
    }
    const actions = el("div", "actions");
    actions.append(button(t(p.error ? "pending.dismiss" : "pending.dontAdd"), () => {
        hidePending();
        act(() => VpmifyService.DiscardAdd(p.token));
    }));
    if (!p.error) {
        const first = p.recipes?.[0]?.id ?? null;
        actions.append(button(t("pending.add"), () => {
            hidePending();
            act(async () => {
                await VpmifyService.ConfirmAdd(p.token);
                selected = first;
            });
        }, {op: true}));
    }
    pendingDialog.append(actions);
}

// recipeNames maps the recipes' ids to their display names, for the
// available versions, which name a recipe by id alone.
let recipeNames = new Map<string, string>();

// entries are the available versions of every recipe, or null when they
// could not be listed.
function showRecipes(recipes: RecipeInfo[], entries: Entry[] | null) {
    recipeNames = new Map(recipes.filter((r) => r.displayName).map((r) => [r.id, r.displayName!]));
    recipesElement.replaceChildren();
    for (const r of recipes) {
        const li = el("li");
        if (r.id === selected) {
            li.className = "selected";
        }
        if (r.origin) {
            li.title = r.origin;
        }
        li.append(label(r.id, r.displayName));
        if (r.problem) {
            const mark = el("span", "error count", "!");
            mark.title = r.problem;
            li.append(mark);
        } else if (entries) {
            const n = entries.filter((e) => e.recipe === r.id).length;
            li.append(n > 0 ? el("span", "ok count", `✔ ${n}`) : el("span", "muted count", "—"));
        }
        li.addEventListener("click", () => {
            selected = r.id;
            clearError();
            refresh();
        });
        recipesElement.append(li);
    }
}

// shownRecipe is the recipe shown on the right, which its buttons act on.
let shownRecipe: RecipeInfo | null = null;

// showRecipe shows recipe r on the right, with what Versions returned for it
// or why it failed.
function showRecipe(r: RecipeInfo | null, hasRecipes: boolean, entries: Entry[] | null, versions: VersionsResult | null, versionsFailure: string) {
    shownRecipe = r;
    noRecipesElement.hidden = hasRecipes;
    recipeElement.hidden = !r;
    if (!r) {
        shown = null;
        return;
    }
    recipeLabel.replaceChildren(label(r.id, r.displayName));
    recipeProblem.hidden = !r.problem;
    recipeProblem.textContent = r.problem ?? "";
    recipeOrigin.textContent = r.origin ?? "";
    recipePackages.textContent = String((r.packages ?? []).length);
    const ofRecipe = entries?.filter((e) => e.recipe === r.id) ?? [];
    recipeAvailable.textContent = entries === null ? "" : ofRecipe.length > 0 ? String(ofRecipe.length) : t("recipe.noneAvailable");
    showVersions(r.id, ofRecipe, versions, versionsFailure);
}

// shown is what showVersions was given, which renderPackages draws again
// when the user opens or closes a package.
let shown: { id: string, packages: PackageVersions[], entries: Entry[] } | null = null;

// open is the packages whose versions are shown, by "<id>/<package>"; seen
// is those shown before, so that a package opens by itself only the first
// time, when it has available versions.
const open = new Set<string>();
const seen = new Set<string>();
// showAll is the packages that show every version, not only the available
// ones and the newest.
const showAll = new Set<string>();

// showVersions shows the versions of recipe id; entries are its available
// versions.
function showVersions(id: string, entries: Entry[], versions: VersionsResult | null, failure: string) {
    // The packages whose source has no usable list are left out, with why.
    const why = failure || versions?.error || "";
    versionsError.hidden = !why;
    versionsError.textContent = why;
    const packages = versions?.list?.packages ?? [];
    // The newest button pulls the recipe without naming a package, which
    // chooses only from the versions shown here (prereleases only when the
    // box shows them) that can be converted, of packages not onDemand.
    newestChoosable = packages.some((p) => !p.onDemand && (p.versions ?? []).some((v) => !v.unusable));
    pullNewestElement.disabled = !newestChoosable || busy;
    shown = {id, packages, entries};
    renderPackages();
}

function renderPackages() {
    if (!shown) {
        packagesElement.replaceChildren();
        return;
    }
    const {id, packages, entries} = shown;
    const blocks = packages.map((p) => packageBlock(id, p.package, p, entries.filter((e) => e.package === p.package)));
    // Packages with available versions but no version list, as when their
    // source could not be read, still show those versions.
    const listed = new Set(packages.map((p) => p.package));
    const unlisted = [...new Set(entries.map((e) => e.package))].filter((name) => !listed.has(name));
    for (const name of unlisted) {
        blocks.push(packageBlock(id, name, null, entries.filter((e) => e.package === name)));
    }
    packagesElement.replaceChildren(...blocks);
    noVersionsElement.hidden = blocks.length > 0;
}

// VersionRow is one row of a package's versions.
interface VersionRow {
    version: string;
    unity?: string;
    origin: string;
    prerelease?: boolean;
    unusable?: string;
    available: boolean;
    // entry is the available version the row removes.
    entry?: Entry;
}

// packageBlock shows package name of recipe id: its version list p, which
// is null when there is none, and its available versions entries.
function packageBlock(id: string, name: string, p: PackageVersions | null, entries: Entry[]): HTMLDivElement {
    const key = `${id}/${name}`;
    if (!seen.has(key)) {
        seen.add(key);
        if (entries.length > 0) {
            open.add(key);
        }
    }
    const displayName = p?.displayName || entries[0]?.displayName;
    const box = el("div", "pkg");
    box.dataset.key = key;
    const head = el("div", "pkg-head");
    head.append(button(open.has(key) ? "▼" : "▶", () => {
        if (!open.delete(key)) {
            open.add(key);
        }
        renderPackages();
    }, {className: "twisty"}));
    const versions = p?.versions ?? [];
    const info = el("div");
    info.append(label(name, displayName));
    if (p) {
        let sub = t("package.info", {
            source: p.source,
            updated: new Date(p.updated).toLocaleString(currentLang()),
            count: versions.length,
        });
        if (p.onDemand) {
            sub += " · " + t("package.onDemand");
        }
        info.append(el("div", "sub", sub));
    }
    head.append(info);
    const summary = el("div", "summary");
    if (entries.length > 0) {
        summary.append(
            el("span", "ok", t("package.available", {count: entries.length})),
            button(t("package.removeAll"), () => confirmPackageRemoval(id, name, displayName, entries), {op: true, className: "small danger"}),
        );
    } else {
        summary.append(el("span", "muted", t("package.noneAvailable")));
    }
    head.append(summary);
    box.append(head);
    if (!open.has(key)) {
        return box;
    }

    // Available versions the version list does not show, such as
    // prereleases while they are hidden and versions upstream no longer
    // has, go last, so that they can be removed one by one too.
    const listed = new Set(versions.map((v) => v.version));
    const rows: VersionRow[] = [
        ...versions.map((v) => ({...v, entry: entries.find((e) => e.version === v.version)})),
        ...entries.filter((e) => !listed.has(e.version)).map((e) => ({
            version: e.version,
            unity: e.unity,
            origin: t("version.notListed"),
            available: true,
            entry: e,
        })),
    ];
    // Folded, a package shows its available versions and its newest.
    const folded = rows.filter((r, i) => r.available || r.entry || i === 0);
    const all = showAll.has(key);

    const table = el("table");
    const cols = el("colgroup");
    cols.append(el("col"), el("col", "unity"), el("col", "from"), el("col", "ops"));
    table.append(cols);
    headRow(table, ["col.version", "col.unity", "col.from", null]);
    const body = table.createTBody();
    for (const r of all ? rows : folded) {
        const row = body.insertRow();
        const vc = row.insertCell();
        vc.append(el("span", "", r.version));
        if (r.available || r.entry) {
            vc.append(el("span", "badge ok", t("version.available")));
        }
        if (r.prerelease) {
            vc.append(el("span", "badge muted", t("version.prerelease")));
        }
        row.insertCell().textContent = r.unity ?? "";
        row.insertCell().textContent = r.origin;
        const op = row.insertCell();
        op.className = "op";
        if (r.entry) {
            const entry = r.entry;
            op.append(button(t("version.remove"), () => confirmVersionRemoval(entry), {op: true, className: "small danger"}));
        } else if (r.unusable) {
            // Why the version cannot be converted.
            op.textContent = r.unusable;
            op.className = "op note";
        } else if (!r.available) {
            op.append(button(t("version.get"), () => act(() => VpmifyService.Pull([`${id}/${name}@${r.version}`], false)), {op: true, className: "small"}));
        }
    }
    if (rows.length > folded.length) {
        const more = body.insertRow();
        more.className = "more";
        const c = more.insertCell();
        c.colSpan = 4;
        c.textContent = all ? t("versions.fewer") : t("versions.more", {count: rows.length - folded.length});
        more.addEventListener("click", () => {
            if (!showAll.delete(key)) {
                showAll.add(key);
            }
            renderPackages();
        });
    }
    box.append(table);
    return box;
}

function showAvailable(entries: Entry[] | null, error: string) {
    availableError.hidden = !error;
    availableError.textContent = error;
    entriesElement.replaceChildren();
    for (const e of entries ?? []) {
        const row = entriesElement.insertRow();
        row.title = e.path;
        row.insertCell().append(label(e.package, e.displayName));
        row.insertCell().textContent = e.version;
        row.insertCell().textContent = e.unity ?? "";
        row.insertCell().append(label(e.recipe, recipeNames.get(e.recipe)));
        const op = row.insertCell();
        op.className = "op";
        op.append(button(t("version.remove"), () => confirmVersionRemoval(e), {op: true, className: "small danger"}));
    }
    emptyElement.hidden = entries === null || entries.length > 0;
}

// generation counts the refreshes. A refresh reads everything before it
// draws, and draws nothing when a newer one has started, so that the
// recipe chosen on the left and what the right shows stay the same one,
// and the right keeps its scroll while it is drawn again.
let generation = 0;

async function refresh() {
    const g = ++generation;
    let entries: Entry[] | null = null;
    let listFailure = "";
    try {
        entries = (await VpmifyService.List()).entries ?? [];
    } catch (err) {
        listFailure = String(err);
    }
    let recipes: RecipeInfo[] = [];
    let recipesFailure: unknown = null;
    try {
        recipes = (await VpmifyService.Recipes()) ?? [];
    } catch (err) {
        recipesFailure = err;
    }
    if (g !== generation) {
        return;
    }
    if (!recipes.some((r) => r.id === selected)) {
        selected = recipes[0]?.id ?? null;
    }
    const id = selected;
    let versions: VersionsResult | null = null;
    let versionsFailure = "";
    if (id !== null) {
        try {
            versions = await VpmifyService.Versions(id, prereleasesElement.checked);
        } catch (err) {
            versionsFailure = String(err);
        }
    }
    if (g !== generation) {
        return;
    }
    if (recipesFailure !== null) {
        showError(recipesFailure);
    }
    showRecipes(recipes, entries);
    showRecipe(recipes.find((r) => r.id === id) ?? null, recipes.length > 0 || recipesFailure !== null, entries, versions, versionsFailure);
    showAvailable(entries, listFailure);
    await showPending().catch(showError);
}

async function showAbout() {
    const a = await VpmifyService.About();
    byId("about-version").textContent = a.version;
    aboutSource.textContent = a.source;
    aboutSource.href = a.source;
    byId("about-license").textContent = a.license;
    byId("about-notices").textContent = a.notices;
}

for (const tab of document.querySelectorAll<HTMLButtonElement>(".tabs button")) {
    tab.addEventListener("click", () => {
        for (const other of document.querySelectorAll<HTMLButtonElement>(".tabs button")) {
            other.setAttribute("aria-selected", String(other === tab));
        }
        for (const panel of document.querySelectorAll<HTMLElement>(".panel")) {
            panel.hidden = panel.id !== `panel-${tab.dataset.panel}`;
        }
    });
}
// Stops what runs and waits; the service says so in the log, not as an
// error, since the user asked for it.
cancelElement.addEventListener("click", () => VpmifyService.Cancel().catch(showError));
byId("choose").addEventListener("click", () => act(() => VpmifyService.ChooseRecipeFiles()));
byId("update-all").addEventListener("click", () => act(() => VpmifyService.Update([])));
byId("update").addEventListener("click", () => {
    if (shownRecipe) {
        const id = shownRecipe.id;
        act(() => VpmifyService.Update([id]));
    }
});
pullNewestElement.addEventListener("click", () => {
    if (shownRecipe) {
        const id = shownRecipe.id;
        // The newest of the versions the list shows, prereleases too when
        // it shows them.
        act(() => VpmifyService.Pull([id], prereleasesElement.checked));
    }
});
byId("remove-recipe").addEventListener("click", () => {
    if (shownRecipe) {
        confirmRecipeRemoval(shownRecipe);
    }
});
// openAll opens or closes every package shown.
function openAll(value: boolean) {
    for (const b of packagesElement.querySelectorAll<HTMLElement>(".pkg")) {
        const key = b.dataset.key!;
        if (value) {
            open.add(key);
        } else {
            open.delete(key);
        }
    }
    renderPackages();
}

byId("expand-all").addEventListener("click", () => openAll(true));
byId("collapse-all").addEventListener("click", () => openAll(false));
byId("reload").addEventListener("click", () => act(async () => {}));
logToggle.addEventListener("click", () => setLogOpen(!logFooter.classList.contains("open")));
// The webview does not navigate away from the app; the link opens in the
// user's browser.
aboutSource.addEventListener("click", (event) => {
    event.preventDefault();
    Browser.OpenURL(aboutSource.href).catch(showError);
});
// The recipes waiting to be added stay until the user adds or discards
// them. Escape is refused, and when the webview closes the dialog anyway,
// as Chromium does for an Escape pressed again, the dialog comes back.
pendingDialog.addEventListener("cancel", (event) => event.preventDefault());
pendingDialog.addEventListener("close", () => {
    if (!hiding) {
        showPending().catch(showError);
    }
    hiding = false;
});
confirmDialog.addEventListener("close", () => {
    confirmDialog.replaceChildren();
    showPending().catch(showError);
});
prereleasesElement.addEventListener("change", () => refresh());
languageElement.addEventListener("change", () => {
    setLang(languageElement.value as Lang);
    applyStatic();
    setLogOpen(logFooter.classList.contains("open"));
    showAbout().catch(showError);
    refresh();
});

async function start() {
    languageElement.value = currentLang();
    applyStatic();
    setLogOpen(false);
    // Listen first, then read what arrived before the page loaded.
    Events.On(Busy, (event) => setBusy(event.data));
    Events.On(Log, (event) => appendLog(event.data));
    Events.On(Pending, () => showPending().catch(showError));
    await showAbout().catch(showError);
    await refresh();
}

start();
