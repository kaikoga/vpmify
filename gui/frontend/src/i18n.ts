// The GUI is released in English and Japanese. Errors from the Go core stay
// in English.

const en = {
    "app.working": "Working",
    "app.addFiles": "Add recipe files…",
    "app.dropHint": "Recipe files dropped on this window are added too, after you confirm.",
    "app.language": "Language",
    "app.about": "About",

    "about.close": "Close",
    "about.source": "Source code:",
    "about.license": "License",
    "about.notices": "Third-party software",

    "pending.title": "Waiting to add",
    "pending.from": "From {from}",
    "pending.anyVersion": "any",
    "pending.replaces": "replaces your recipe",
    "pending.new": "new",
    "pending.add": "Add",
    "pending.dontAdd": "Don't add",
    "pending.dismiss": "Dismiss",

    "recipes.title": "Recipes",
    "recipes.updateAll": "Update all",
    "recipes.none": "No recipes yet.",

    "versions.title": "Versions of {id}",
    "versions.prereleases": "Prereleases",
    "versions.update": "Update",
    "versions.pullNewest": "Make newest available",
    "versions.removeRecipe": "Remove recipe…",
    "versions.none": "No version to show.",
    "versions.prerelease": "{version} (prerelease)",
    "versions.listRead": "{package} ({source}), version list read {updated}",
    "versions.availableYes": "yes",
    "versions.makeAvailable": "Make available",
    "versions.remove": "Remove…",
    "versions.removeAll": "Remove all versions…",

    "available.title": "Available versions",
    "available.reload": "Reload",
    "available.none": "No version is available yet.",
    "available.registered": "registered",
    "available.notRegistered": "not registered",
    "available.settingsError": "ALCOM settings: {error}",

    "log.title": "Log",

    "col.recipe": "Recipe",
    "col.packages": "Packages",
    "col.package": "Package",
    "col.problem": "Problem",
    "col.source": "Source",
    "col.versions": "Versions",
    "col.version": "Version",
    "col.unity": "Unity",
    "col.from": "From",
    "col.available": "Available",
    "col.alcom": "ALCOM",

    "remove.cancel": "Cancel",
    "remove.recipe.title": "Remove the recipe {id}",
    "remove.recipe.before": "These packages the recipe made available are removed from ALCOM.",
    "remove.recipe.beforeNone": "The recipe has made no package available; only the recipe is removed.",
    "remove.recipe.after": "If a project has imported them, ALCOM can no longer update or reinstall them.",
    "remove.recipe.confirm": "Remove recipe",
    "remove.version.title": "Remove {label} {version}",
    "remove.version.before": "{label} {version} is removed from ALCOM.",
    "remove.version.after": "If a project has imported it, ALCOM can no longer reinstall it. It can be made available again here.",
    "remove.version.confirm": "Remove package",
    "remove.package.title": "Remove every version of {label}",
    "remove.package.before": "These versions of {label} are removed from ALCOM.",
    "remove.package.after": "If a project has imported them, ALCOM can no longer update or reinstall them. The recipe stays, so they can be made available again here.",
    "remove.package.confirm": "Remove versions",
};

export type Key = keyof typeof en;

// The texts of the removal cards follow kaikoga's draft (2026-11-19).
const ja: Record<Key, string> = {
    "app.working": "処理中",
    "app.addFiles": "レシピのファイルを追加…",
    "app.dropHint": "この窓にドロップしたレシピのファイルも、確認してから追加します。",
    "app.language": "言語",
    "app.about": "このアプリについて",

    "about.close": "閉じる",
    "about.source": "ソースコード:",
    "about.license": "ライセンス",
    "about.notices": "サードパーティのソフトウェア",

    "pending.title": "追加の確認",
    "pending.from": "{from} から",
    "pending.anyVersion": "すべて",
    "pending.replaces": "今のレシピを置き換える",
    "pending.new": "新規",
    "pending.add": "追加する",
    "pending.dontAdd": "追加しない",
    "pending.dismiss": "閉じる",

    "recipes.title": "レシピ",
    "recipes.updateAll": "すべて更新",
    "recipes.none": "レシピはまだありません。",

    "versions.title": "{id} のバージョン",
    "versions.prereleases": "プレリリース",
    "versions.update": "更新",
    "versions.pullNewest": "最新版をインストール可能にする",
    "versions.removeRecipe": "レシピを削除…",
    "versions.none": "表示するバージョンはありません。",
    "versions.prerelease": "{version}（プレリリース）",
    "versions.listRead": "{package}（{source}）、バージョンの一覧は {updated} に取得",
    "versions.availableYes": "可",
    "versions.makeAvailable": "インストール可能にする",
    "versions.remove": "削除…",
    "versions.removeAll": "全バージョンを削除…",

    "available.title": "インストール可能なバージョン",
    "available.reload": "再読み込み",
    "available.none": "インストール可能なバージョンはまだありません。",
    "available.registered": "登録済み",
    "available.notRegistered": "未登録",
    "available.settingsError": "ALCOM の設定: {error}",

    "log.title": "ログ",

    "col.recipe": "レシピ",
    "col.packages": "パッケージ",
    "col.package": "パッケージ",
    "col.problem": "問題",
    "col.source": "取得元",
    "col.versions": "バージョン",
    "col.version": "バージョン",
    "col.unity": "Unity",
    "col.from": "元",
    "col.available": "インストール可能",
    "col.alcom": "ALCOM",

    "remove.cancel": "キャンセル",
    "remove.recipe.title": "{id} レシピの削除",
    "remove.recipe.before": "レシピによってインストールされた以下のパッケージが ALCOM から削除されます。",
    "remove.recipe.beforeNone": "このレシピがインストール可能にしたパッケージはありません。レシピだけを削除します。",
    "remove.recipe.after": "これらのパッケージがプロジェクトにインポートされていた場合、ALCOM でアップデートや再インストールを行えなくなります。",
    "remove.recipe.confirm": "レシピを削除する",
    "remove.version.title": "{label} {version} パッケージの削除",
    "remove.version.before": "{label} {version} パッケージが ALCOM から削除されます。",
    "remove.version.after": "このパッケージがプロジェクトにインポートされていた場合、ALCOM で再インストールを行えなくなります。ここからもう一度インストール可能にできます。",
    "remove.version.confirm": "パッケージを削除する",
    "remove.package.title": "{label} の全バージョンの削除",
    "remove.package.before": "{label} の以下のバージョンが ALCOM から削除されます。",
    "remove.package.after": "これらのパッケージがプロジェクトにインポートされていた場合、ALCOM でアップデートや再インストールを行えなくなります。レシピは残るので、ここからもう一度インストール可能にできます。",
    "remove.package.confirm": "バージョンを削除する",
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
export function t(key: Key, params: Record<string, string> = {}): string {
    return messages[lang][key].replace(/\{(\w+)\}/g, (m, name: string) => params[name] ?? m);
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
