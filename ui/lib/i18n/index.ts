import i18next, { type BackendModule } from "i18next";
import { initReactI18next } from "react-i18next";

import { detectInitialLanguage, persistLanguage, type Language } from "./detection";
import { loadNamespace } from "./lazyResources";
import type { Namespace } from "./namespaces";

export { SUPPORTED_LANGUAGES } from "./detection";
export type { Language } from "./detection";

/** Language autonyms — always shown in their own language, never translated. */
export const LANGUAGE_DISPLAY_NAMES: Record<Language, string> = {
	en: "English",
	"zh-CN": "中文",
};

const lazyBackend: BackendModule = {
	type: "backend",
	init() {},
	read(language, namespace, callback) {
		loadNamespace(language as Language, namespace as Namespace)
			.then((resources) => callback(null, resources))
			.catch((error) => callback(error, null));
	},
};

void i18next
	.use(initReactI18next)
	.use(lazyBackend)
	.init({
		lng: detectInitialLanguage(),
		fallbackLng: "en",
		defaultNS: "common",
		// i18next resolves a missing key through fallbackLng, reading en/<ns>.json
		// through the same lazy backend — the "load both, then render" behavior the
		// spec requires, with no extra wiring.
		ns: ["common"],
		interpolation: { escapeValue: false },
		react: { useSuspense: false },
		missingKeyHandler: (_lngs, ns, key) => {
			if (import.meta.env.DEV) console.warn(`[i18n] missing key "${ns}:${key}"`);
		},
	});

export function changeLanguage(language: Language): void {
	persistLanguage(language);
	void i18next.changeLanguage(language);
}

export default i18next;