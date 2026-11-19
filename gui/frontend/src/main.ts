import {Events} from "@wailsio/runtime";
import {VpmifyService} from "../bindings/github.com/kaikoga/vpmify/gui";
import {Busy, Log, Pending} from "../bindings/events";

function byId<T extends HTMLElement>(id: string): T {
    return document.getElementById(id)! as T;
}

const errorElement = byId<HTMLParagraphElement>("error");
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
    let shown = 0;
    for (const p of list.packages ?? []) {
        // A heading row for each package, as a recipe may have several.
        const heading = versionsElement.insertRow();
        heading.className = "group";
        const th = document.createElement("th");
        th.colSpan = 6;
        th.textContent = `${p.package} (${p.source}), version list read ${new Date(p.updated).toLocaleString()}`;
        heading.appendChild(th);
        for (const v of p.versions ?? []) {
            const row = versionsElement.insertRow();
            cell(row, v.version + (v.prerelease ? " (prerelease)" : ""));
            cell(row, v.unity ?? "");
            cell(row, v.origin);
            cell(row, v.unusable ? "-" : v.newest ? "yes" : "");
            cell(row, v.available ? "yes" : "");
            const td = row.insertCell();
            if (v.unusable) {
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
            cell(row, e.package);
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
