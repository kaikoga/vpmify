import {Events} from "@wailsio/runtime";
import {VpmifyService} from "../bindings/github.com/kaikoga/vpmify/gui";
import type {Entry} from "../bindings/github.com/kaikoga/vpmify/internal/app";
import {Busy, Log, Pending} from "../bindings/events";

function byId<T extends HTMLElement>(id: string): T {
    return document.getElementById(id)! as T;
}

const errorElement = byId<HTMLParagraphElement>("error");
const confirmSection = byId<HTMLElement>("confirm-section");
const confirmElement = byId<HTMLDivElement>("confirm");
const spinnerElement = byId<HTMLSpanElement>("spinner");
const pendingSection = byId<HTMLElement>("pending-section");
const pendingElement = byId<HTMLDivElement>("pending");
const recipesElement = byId<HTMLTableSectionElement>("recipes");
const noRecipesElement = byId<HTMLParagraphElement>("no-recipes");
const versionsSection = byId<HTMLElement>("versions-section");
const versionsTitle = byId<HTMLHeadingElement>("versions-title");
const versionsUpdated = byId<HTMLParagraphElement>("versions-updated");
const versionsElement = byId<HTMLTableSectionElement>("versions");
const noVersionsElement = byId<HTMLParagraphElement>("no-versions");
const prereleasesElement = byId<HTMLInputElement>("prereleases");
const entriesElement = byId<HTMLTableSectionElement>("entries");
const emptyElement = byId<HTMLParagraphElement>("empty");
const settingsErrorElement = byId<HTMLParagraphElement>("settings-error");
const logElement = byId<HTMLPreElement>("log");

// selected is the recipe whose versions are shown.
let selected: string | null = null;

function cell(row: HTMLTableRowElement, text: string): HTMLTableCellElement {
    const td = row.insertCell();
    td.textContent = text;
    return td;
}

function button(label: string, onClick: () => void): HTMLButtonElement {
    const b = document.createElement("button");
    b.type = "button";
    b.textContent = label;
    b.addEventListener("click", onClick);
    return b;
}

// packageLabel names a package with its display name first, as ALCOM shows
// both (kaikoga 2026-11-19).
function packageLabel(name: string, displayName?: string): string {
    return displayName ? `${displayName} (${name})` : name;
}

// Removal is what a confirmation card asks about.
interface Removal {
    title: string;
    // before and after go above and below the table of versions.
    before: string;
    entries: Entry[];
    after: string;
    confirm: string;
    run: () => Promise<unknown>;
}

// confirmRemoval shows a card that removes only when the user confirms
// (kaikoga 2026-11-19: removing is destructive, so the GUI asks).
function confirmRemoval(r: Removal) {
    confirmElement.replaceChildren();
    const title = document.createElement("h3");
    title.textContent = r.title;
    confirmElement.appendChild(title);
    const before = document.createElement("p");
    before.textContent = r.before;
    confirmElement.appendChild(before);
    if (r.entries.length > 0) {
        const table = document.createElement("table");
        const head = table.createTHead().insertRow();
        for (const h of ["Package", "Version", "Unity"]) {
            const th = document.createElement("th");
            th.textContent = h;
            head.appendChild(th);
        }
        const body = table.createTBody();
        for (const e of r.entries) {
            const row = body.insertRow();
            cell(row, packageLabel(e.package, e.displayName));
            cell(row, e.version);
            cell(row, e.unity ?? "");
        }
        confirmElement.appendChild(table);
    }
    const after = document.createElement("p");
    after.textContent = r.after;
    confirmElement.appendChild(after);
    const actions = document.createElement("div");
    actions.className = "actions";
    actions.appendChild(button("Cancel", closeConfirm));
    actions.appendChild(button(r.confirm, () => {
        closeConfirm();
        act(r.run);
    }));
    confirmElement.appendChild(actions);
    confirmSection.hidden = false;
    confirmSection.scrollIntoView({block: "center"});
}

function closeConfirm() {
    confirmSection.hidden = true;
    confirmElement.replaceChildren();
}

async function confirmRecipeRemoval(id: string) {
    clearError();
    let preview;
    try {
        preview = await VpmifyService.RecipeRemoval(id);
    } catch (err) {
        showError(err);
        return;
    }
    const entries = preview.removed ?? [];
    confirmRemoval({
        title: `Remove the recipe ${id}`,
        before: entries.length > 0
            ? "These packages the recipe made available are removed from ALCOM."
            : "The recipe has made no package available; only the recipe is removed.",
        entries,
        after: entries.length > 0
            ? "If a project has imported them, ALCOM can no longer update or reinstall them."
            : "",
        confirm: "Remove recipe",
        run: () => VpmifyService.RemoveRecipe(id),
    });
}

function confirmVersionRemoval(id: string, e: Entry) {
    const label = packageLabel(e.package, e.displayName);
    confirmRemoval({
        title: `Remove ${label} ${e.version}`,
        before: `${label} ${e.version} is removed from ALCOM.`,
        entries: [],
        after: "If a project has imported it, ALCOM can no longer reinstall it. It can be made available again here.",
        confirm: "Remove package",
        run: () => VpmifyService.RemoveVersions(`${id}/${e.package}`, e.version),
    });
}

function confirmPackageRemoval(id: string, name: string, entries: Entry[]) {
    const label = packageLabel(name, entries[0]?.displayName);
    confirmRemoval({
        title: `Remove every version of ${label}`,
        before: `These versions of ${label} are removed from ALCOM.`,
        entries,
        after: "If a project has imported them, ALCOM can no longer update or reinstall them. The recipe stays, so they can be made available again here.",
        confirm: "Remove versions",
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

function setBusy(busy: boolean) {
    spinnerElement.hidden = !busy;
    for (const b of document.querySelectorAll<HTMLButtonElement>("button")) {
        b.disabled = busy;
    }
}

function appendLog(line: string) {
    logElement.textContent += line + "\n";
    logElement.scrollTop = logElement.scrollHeight;
}

async function showPending() {
    const pending = (await VpmifyService.Pending()) ?? [];
    pendingElement.replaceChildren();
    for (const p of pending) {
        const card = document.createElement("div");
        card.className = "card";
        const from = document.createElement("p");
        from.className = "note";
        from.textContent = "From " + (p.from ?? []).join(", ");
        card.appendChild(from);
        if (p.error) {
            const e = document.createElement("p");
            e.className = "error";
            e.textContent = p.error;
            card.appendChild(e);
        } else {
            const table = document.createElement("table");
            const head = table.createTHead().insertRow();
            for (const h of ["Recipe", "Package", "Source", "Versions", ""]) {
                const th = document.createElement("th");
                th.textContent = h;
                head.appendChild(th);
            }
            const body = table.createTBody();
            // One row per package; the recipe's id and whether it is new go
            // on its first row.
            for (const r of p.recipes ?? []) {
                (r.packages ?? []).forEach((pkg, i) => {
                    const row = body.insertRow();
                    row.title = r.origin;
                    cell(row, i === 0 ? r.id : "");
                    cell(row, pkg.name);
                    cell(row, pkg.source);
                    cell(row, pkg.versions || "any");
                    cell(row, i === 0 ? (r.replaces ? "replaces your recipe" : "new") : "");
                });
            }
            card.appendChild(table);
        }
        const actions = document.createElement("div");
        actions.className = "actions";
        if (!p.error) {
            const first = p.recipes?.[0]?.id ?? null;
            actions.appendChild(button("Add", () => act(async () => {
                await VpmifyService.ConfirmAdd(p.token);
                selected = first;
            })));
        }
        actions.appendChild(button(p.error ? "Dismiss" : "Don't add", () => act(() => VpmifyService.DiscardAdd(p.token))));
        card.appendChild(actions);
        pendingElement.appendChild(card);
    }
    pendingSection.hidden = pending.length === 0;
}

async function showRecipes() {
    const recipes = (await VpmifyService.Recipes()) ?? [];
    recipesElement.replaceChildren();
    for (const r of recipes) {
        const row = recipesElement.insertRow();
        row.className = "selectable";
        if (r.id === selected) {
            row.classList.add("selected");
        }
        if (r.origin) {
            row.title = r.origin;
        }
        cell(row, r.id);
        cell(row, (r.packages ?? []).join(", "));
        cell(row, r.problem ?? "").className = "error";
        row.addEventListener("click", async () => {
            selected = r.id;
            // A card about the recipe shown before would remove from the
            // wrong recipe's view.
            closeConfirm();
            clearError();
            await refresh();
        });
    }
    noRecipesElement.hidden = recipes.length > 0;
    if (selected !== null && !recipes.some((r) => r.id === selected)) {
        selected = null;
    }
}

async function showVersions() {
    versionsSection.hidden = selected === null;
    if (selected === null) {
        return;
    }
    const id = selected;
    versionsTitle.textContent = `Versions of ${id}`;
    versionsElement.replaceChildren();
    versionsUpdated.textContent = "";
    noVersionsElement.hidden = true;
    let result;
    try {
        result = await VpmifyService.Versions(id, prereleasesElement.checked);
    } catch (err) {
        versionsUpdated.textContent = String(err);
        return;
    }
    const list = result.list;
    if (!list) {
        return;
    }
    // The packages whose source has no usable list are left out, with why.
    versionsUpdated.textContent = result.error ?? "";
    // The available versions of the recipe, hidden prereleases too, which
    // the remove buttons name. showAvailable reports a failure to list.
    let available: Entry[] = [];
    try {
        available = ((await VpmifyService.List()).entries ?? []).filter((e) => e.recipe === id);
    } catch {
        available = [];
    }
    let shown = 0;
    for (const p of list.packages ?? []) {
        const ofPackage = available.filter((e) => e.package === p.package);
        // A heading row for each package, as a recipe may have several.
        const heading = versionsElement.insertRow();
        heading.className = "group";
        const th = document.createElement("th");
        th.colSpan = 4;
        th.textContent = `${packageLabel(p.package, p.displayName)} (${p.source}), version list read ${new Date(p.updated).toLocaleString()}`;
        heading.appendChild(th);
        const headingActions = heading.insertCell();
        if (ofPackage.length > 0) {
            headingActions.appendChild(button("Remove all versions…", () => confirmPackageRemoval(id, p.package, ofPackage)));
        }
        for (const v of p.versions ?? []) {
            const row = versionsElement.insertRow();
            cell(row, v.version + (v.prerelease ? " (prerelease)" : ""));
            cell(row, v.unity ?? "");
            cell(row, v.origin);
            cell(row, v.available ? "yes" : "");
            const td = row.insertCell();
            const entry = ofPackage.find((e) => e.version === v.version);
            if (entry) {
                td.appendChild(button("Remove…", () => confirmVersionRemoval(id, entry)));
            } else if (v.unusable) {
                // Why the version cannot be converted.
                td.textContent = v.unusable;
                td.className = "note";
            } else if (!v.available) {
                td.appendChild(button("Make available", () => act(() => VpmifyService.Pull([`${id}/${p.package}@${v.version}`]))));
            }
            shown++;
        }
    }
    noVersionsElement.hidden = shown > 0;
}

async function showAvailable() {
    try {
        const result = await VpmifyService.List();
        const entries = result.entries ?? [];
        entriesElement.replaceChildren();
        for (const e of entries) {
            const row = entriesElement.insertRow();
            row.title = e.path;
            cell(row, e.recipe);
            cell(row, packageLabel(e.package, e.displayName));
            cell(row, e.version);
            cell(row, e.unity ?? "");
            cell(row, e.registered ? "registered" : "not registered");
        }
        emptyElement.hidden = entries.length > 0;
        settingsErrorElement.hidden = !result.settingsError;
        settingsErrorElement.textContent = result.settingsError ? `ALCOM settings: ${result.settingsError}` : "";
    } catch (err) {
        settingsErrorElement.hidden = false;
        settingsErrorElement.textContent = String(err);
    }
}

async function refresh() {
    try {
        await showPending();
        await showRecipes();
        await showVersions();
    } catch (err) {
        showError(err);
    }
    await showAvailable();
}

byId("choose").addEventListener("click", () => act(() => VpmifyService.ChooseRecipeFiles()));
byId("update-all").addEventListener("click", () => act(() => VpmifyService.Update([])));
byId("update").addEventListener("click", () => {
    if (selected !== null) {
        act(() => VpmifyService.Update([selected!]));
    }
});
byId("pull-newest").addEventListener("click", () => {
    if (selected !== null) {
        act(() => VpmifyService.Pull([selected!]));
    }
});
byId("remove-recipe").addEventListener("click", () => {
    if (selected !== null) {
        confirmRecipeRemoval(selected);
    }
});
byId("reload").addEventListener("click", () => act(async () => {}));
prereleasesElement.addEventListener("change", () => showVersions());

async function start() {
    // Listen first, then read what arrived before the page loaded.
    Events.On(Busy, (event) => setBusy(event.data));
    Events.On(Log, (event) => appendLog(event.data));
    Events.On(Pending, () => showPending().catch(showError));
    await refresh();
}

start();
