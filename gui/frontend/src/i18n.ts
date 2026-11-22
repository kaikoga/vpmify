// The GUI is released in English and Japanese. Errors from the Go core stay
// in English.

const en = {
    "app.working": "Working",
    "app.cancel": "Cancel",
    "app.addFiles": "Add from files…",
    "app.language": "Language",

    "tab.recipes": "Recipes",
    "tab.available": "Available versions",
    "tab.about": "About",

    "about.source": "Source code:",
    "about.license": "License",
    "about.notices": "Third-party software",

    "pending.title": "Add recipes?",
    "pending.from": "From {from}",
    "pending.more": "{count} more waiting after this.",
    "pending.anyVersion": "any",
    "pending.replaces": "update",
    "pending.new": "new",
    "pending.add": "Add",
    "pending.dontAdd": "Don't add",
    "pending.dismiss": "Dismiss",

    "recipes.updateAll": "Update all recipes",
    "recipes.none": "No recipes yet. Add recipe files from the button at the top, or drop them on this window.",

    "recipe.update": "Update",
    "recipe.pullNewest": "Get the newest version",
    "recipe.remove": "Remove the recipe",
    "recipe.origin": "Recipe from",
    "recipe.packages": "Packages",
    "recipe.available": "Available versions",
    "recipe.noneAvailable": "none",
    "recipe.newer": "An update can be got",
    "recipe.prereleases": "Show prereleases",
    "recipe.expandAll": "Expand all",
    "recipe.collapseAll": "Collapse all",

    "package.info": "{source} · Read {updated} · {count} versions",
    "package.onDemand": "Optional package",
    "package.available": "✔ {count} available",
    "package.noneAvailable": "None available",
    "package.removeAll": "Remove all",

    "versions.none": "No version to show.",
    "versions.more": "Show {count} more versions",
    "versions.fewer": "Show fewer",
    "version.available": "Available",
    "version.prerelease": "Prerelease",
    "version.notListed": "not in the version list",
    "version.get": "Get",
    "version.remove": "Remove",

    "available.reload": "Reload",
    "available.none": "No version is available yet.",

    "log.title": "Log",
    "log.expand": "Expand",
    "log.shrink": "Shrink",

    "col.recipe": "Recipe",
    "col.package": "Package",
    "col.source": "Source",
    "col.versions": "Versions",
    "col.version": "Version",
    "col.unity": "Unity",
    "col.from": "From",

    "remove.cancel": "Cancel",
    "remove.recipe.title": "Remove the recipe {label}",
    "remove.recipe.before": "These packages the recipe made available are removed from ALCOM.",
    "remove.recipe.beforeNone": "The recipe has made no package available; only the recipe is removed.",
    "remove.recipe.after": "If a project has imported them, ALCOM can no longer update or reinstall them.",
    "remove.recipe.confirm": "Remove recipe",
    "remove.version.title": "Remove {label} {version}",
    "remove.version.before": "{label} {version} is removed from ALCOM.",
    "remove.version.after": "If a project has imported it, ALCOM can no longer reinstall it. You can get it here again.",
    "remove.version.confirm": "Remove package",
    "remove.package.title": "Remove every version of {label}",
    "remove.package.before": "These versions of {label} are removed from ALCOM.",
    "remove.package.after": "If a project has imported them, ALCOM can no longer update or reinstall them. The recipe stays, so you can get them here again.",
    "remove.package.confirm": "Remove versions",

    // The failures the service names by kind, in gui/messages.go.
    "error.noList": "The recipe {recipe} has no version list. Press Update on that recipe.",
    "error.notListed": "{version} is not in the version list of the recipe {recipe}. Press Update on that recipe, then get it again.",
    "error.noMatch": "The version list of the recipe {recipe} has no version of {package} that satisfies {range}. Press Update on that recipe, then get it again.",
    "error.noRecipe": "There is no recipe {recipe}.",
    "error.linkUnknown": "This is not a Vpmify link: {link}",
    "error.linkNoRecipe": "The link names no recipe: {link}",
    "error.linkNotHttps": "The recipe in the link is not an https URL: {url}",
    "error.pendingGone": "This confirmation is already closed.",
};

export type Key = keyof typeof en;

// The texts of the removal dialogs follow kaikoga's draft, with "install"
// left out as the rest of the GUI does.
const ja: Record<Key, string> = {
    "app.working": "処理中",
    "app.cancel": "キャンセル",
    "app.addFiles": "ファイルから追加…",
    "app.language": "言語",

    "tab.recipes": "レシピ",
    "tab.available": "利用可能なバージョン",
    "tab.about": "このアプリについて",

    "about.source": "ソースコード:",
    "about.license": "ライセンス",
    "about.notices": "サードパーティのソフトウェア",

    "pending.title": "追加の確認",
    "pending.from": "{from} から",
    "pending.more": "このあとに {count} 件が待っています。",
    "pending.anyVersion": "すべて",
    "pending.replaces": "更新",
    "pending.new": "新規",
    "pending.add": "追加する",
    "pending.dontAdd": "追加しない",
    "pending.dismiss": "閉じる",

    "recipes.updateAll": "全てのレシピを更新",
    "recipes.none": "レシピはまだありません。上のボタンか、この窓へのドロップで、レシピのファイルを追加してください。",

    "recipe.update": "更新",
    "recipe.pullNewest": "最新版を取得する",
    "recipe.remove": "レシピを取り除く",
    "recipe.origin": "レシピの取得元",
    "recipe.packages": "パッケージ",
    "recipe.available": "利用可能なバージョン",
    "recipe.noneAvailable": "なし",
    "recipe.newer": "アップデートを取得できます",
    "recipe.prereleases": "プレリリース版を表示",
    "recipe.expandAll": "全て開く",
    "recipe.collapseAll": "全て閉じる",

    "package.info": "{source} · 取得日時：{updated} · バージョン数：{count}",
    "package.onDemand": "任意パッケージ",
    "package.available": "✔ 利用可能なバージョン {count}",
    "package.noneAvailable": "利用可能なバージョンなし",
    "package.removeAll": "全て取り除く",

    "versions.none": "表示するバージョンはありません。",
    "versions.more": "ほか {count} 個のバージョンを見る",
    "versions.fewer": "閉じる",
    "version.available": "利用可能",
    "version.prerelease": "プレリリース",
    "version.notListed": "バージョンの一覧にない",
    "version.get": "取得する",
    "version.remove": "取り除く",

    "available.reload": "再読み込み",
    "available.none": "利用可能なバージョンはまだありません。",

    "log.title": "ログ",
    "log.expand": "広げる",
    "log.shrink": "狭める",

    "col.recipe": "レシピ",
    "col.package": "パッケージ",
    "col.source": "取得元",
    "col.versions": "バージョン",
    "col.version": "バージョン",
    "col.unity": "Unity",
    "col.from": "出どころ",

    "remove.cancel": "キャンセル",
    "remove.recipe.title": "{label} レシピの削除",
    "remove.recipe.before": "レシピで取得した以下のパッケージが ALCOM から削除されます。",
    "remove.recipe.beforeNone": "このレシピで取得したパッケージはありません。レシピだけを削除します。",
    "remove.recipe.after": "これらのパッケージがプロジェクトにインポートされていた場合、ALCOM でアップデートや再インストールを行えなくなります。",
    "remove.recipe.confirm": "レシピを削除する",
    "remove.version.title": "{label} {version} パッケージの削除",
    "remove.version.before": "{label} {version} パッケージが ALCOM から削除されます。",
    "remove.version.after": "このパッケージがプロジェクトにインポートされていた場合、ALCOM で再インストールを行えなくなります。ここからもう一度取得できます。",
    "remove.version.confirm": "パッケージを削除する",
    "remove.package.title": "{label} の全バージョンの削除",
    "remove.package.before": "{label} の以下のバージョンが ALCOM から削除されます。",
    "remove.package.after": "これらのパッケージがプロジェクトにインポートされていた場合、ALCOM でアップデートや再インストールを行えなくなります。レシピは残るので、ここからもう一度取得できます。",
    "remove.package.confirm": "バージョンを削除する",

    "error.noList": "レシピ {recipe} のバージョンの一覧がありません。そのレシピの「更新」を押してください。",
    "error.notListed": "{version} は、レシピ {recipe} のバージョンの一覧にありません。そのレシピの「更新」を押してから、もう一度取得してください。",
    "error.noMatch": "レシピ {recipe} のバージョンの一覧に、{package} の {range} に合うバージョンがありません。そのレシピの「更新」を押してから、もう一度取得してください。",
    "error.noRecipe": "レシピ {recipe} はありません。",
    "error.linkUnknown": "Vpmify のリンクではありません: {link}",
    "error.linkNoRecipe": "リンクにレシピがありません: {link}",
    "error.linkNotHttps": "リンクのレシピの URL が https ではありません: {url}",
    "error.pendingGone": "この追加の確認は終わっています。",
};

export type Lang = "en" | "ja";

const messages: Record<Lang, Record<Key, string>> = {en, ja};

const storageKey = "vpmify.lang";

// detect picks the language the user chose before, or else the first of the
// browser's languages the GUI has. Storage may be unavailable, which only
// loses the choice.
function detect(): Lang {
    try {
        const saved = localStorage.getItem(storageKey);
        if (saved === "en" || saved === "ja") {
            return saved;
        }
    } catch {
        // fall through to the browser's languages
    }
    for (const l of navigator.languages ?? [navigator.language]) {
        if (l.toLowerCase().startsWith("ja")) {
            return "ja";
        }
        if (l.toLowerCase().startsWith("en")) {
            return "en";
        }
    }
    return "en";
}

let lang: Lang = detect();

export function currentLang(): Lang {
    return lang;
}

export function setLang(l: Lang) {
    lang = l;
    try {
        localStorage.setItem(storageKey, l);
    } catch {
        // the choice lasts until the window closes
    }
}

// t returns the text of key in the current language, with each {name}
// replaced by params[name].
export function t(key: Key, params: Record<string, string | number> = {}): string {
    return messages[lang][key].replace(/\{(\w+)\}/g, (m, name: string) => String(params[name] ?? m));
}

// isKey says whether s is a key of the texts.
export function isKey(s: string): s is Key {
    return Object.hasOwn(en, s);
}

// applyStatic sets the texts of the page's elements that name a key in
// data-i18n, and the titles of those that name one in data-i18n-title.
export function applyStatic() {
    document.documentElement.lang = lang;
    for (const el of document.querySelectorAll<HTMLElement>("[data-i18n]")) {
        el.textContent = t(el.dataset.i18n as Key);
    }
    for (const el of document.querySelectorAll<HTMLElement>("[data-i18n-title]")) {
        el.title = t(el.dataset.i18nTitle as Key);
    }
}
