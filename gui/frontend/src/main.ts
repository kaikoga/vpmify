import {Events} from "@wailsio/runtime";
import {VpmifyService} from "../bindings/github.com/kaikoga/vpmify/gui";
import {Url} from "../bindings/events";

const entriesElement = document.getElementById("entries")! as HTMLTableSectionElement;
const emptyElement = document.getElementById("empty")! as HTMLParagraphElement;
const settingsErrorElement = document.getElementById("settings-error")! as HTMLParagraphElement;
const urlsElement = document.getElementById("urls")! as HTMLUListElement;
const noUrlsElement = document.getElementById("no-urls")! as HTMLParagraphElement;

function cell(row: HTMLTableRowElement, text: string) {
    const td = row.insertCell();
    td.textContent = text;
}

async function reload() {
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

function addUrl(url: string) {
    const li = document.createElement("li");
    li.textContent = url;
    urlsElement.appendChild(li);
    noUrlsElement.hidden = true;
}

document.getElementById("reload")!.addEventListener("click", reload);

async function start() {
    // Listen first, then fetch the URLs that arrived before the page loaded.
    Events.On(Url, (event) => addUrl(event.data));
    for (const url of (await VpmifyService.ReceivedURLs()) ?? []) {
        addUrl(url);
    }
    await reload();
}

start();
