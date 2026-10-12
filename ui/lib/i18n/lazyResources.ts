import type { Language } from "./detection";
import type { Namespace } from "./namespaces";

// Vite glob import: no per-file registry to maintain. Locale files added by later
// batches are picked up automatically at build time.
const modules = import.meta.glob<{ default: object }>("./locales/*/*.json");

export async function loadNamespace(language: Language, namespace: Namespace): Promise<object> {
	const load = modules[`./locales/${language}/${namespace}.json`];
	if (!load) throw new Error(`Missing locale file: ${language}/${namespace}.json`);
	const mod = await load();
	return mod.default;
}