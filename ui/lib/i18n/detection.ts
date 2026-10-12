export const STORAGE_KEY = "bifrost-language";

export const SUPPORTED_LANGUAGES = ["en", "zh-CN"] as const;
export type Language = (typeof SUPPORTED_LANGUAGES)[number];
export const DEFAULT_LANGUAGE: Language = "en";

export function isSupportedLanguage(value: string | null | undefined): value is Language {
	return value === "en" || value === "zh-CN";
}

/**
 * Pure decision logic: stored preference wins, then the first browser language
 * whose prefix matches a supported locale, then the default. Kept DOM-free so it
 * is unit-testable in a plain node vitest environment.
 */
export function detectLanguage(stored: string | null | undefined, browserLanguages: readonly string[] | undefined): Language {
	if (isSupportedLanguage(stored)) return stored;
	for (const tag of browserLanguages ?? []) {
		const lower = tag.toLowerCase();
		if (lower.startsWith("zh")) return "zh-CN";
		if (lower.startsWith("en")) return "en";
	}
	return DEFAULT_LANGUAGE;
}

export function readStoredLanguage(): string | null {
	try {
		return window.localStorage.getItem(STORAGE_KEY);
	} catch {
		return null;
	}
}

export function persistLanguage(language: Language): void {
	try {
		window.localStorage.setItem(STORAGE_KEY, language);
	} catch {
		// Storage disabled (private mode): preference just won't survive reloads.
	}
}

export function detectInitialLanguage(): Language {
	let browser: readonly string[] = [];
	try {
		browser = navigator.languages ?? [navigator.language];
	} catch {
		// Non-browser environment: fall through to default.
	}
	return detectLanguage(readStoredLanguage(), browser);
}