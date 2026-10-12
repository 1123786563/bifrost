import { describe, expect, it } from "vitest";

const localeModules = import.meta.glob<{ default: Record<string, unknown> }>("./locales/*/*.json", {
	eager: true,
});

function flattenKeys(value: unknown, prefix = ""): string[] {
	if (value === null || typeof value !== "object") return prefix ? [prefix] : [];
	return Object.entries(value).flatMap(([key, child]) => flattenKeys(child, prefix ? `${prefix}.${key}` : key));
}

const files = Object.entries(localeModules);
const languages = new Set(files.map(([path]) => path.split("/")[2]));
const namespaces = new Set(files.map(([path]) => path.split("/")[3].replace(/\.json$/, "")));

describe("locale parity", () => {
	it("every namespace exists in every language", () => {
		for (const ns of namespaces) {
			for (const lng of languages) {
				const file = files.find(([path]) => path === `./locales/${lng}/${ns}.json`);
				expect(file, `missing ${lng}/${ns}.json`).toBeDefined();
			}
		}
	});

	it("zh-CN key structure matches en exactly, for every namespace", () => {
		for (const ns of namespaces) {
			const en = localeModules[`./locales/en/${ns}.json`]?.default;
			const zh = localeModules[`./locales/zh-CN/${ns}.json`]?.default;
			expect(en, `missing en/${ns}.json`).toBeDefined();
			expect(zh, `missing zh-CN/${ns}.json`).toBeDefined();
			expect(flattenKeys(zh).sort(), `key drift in ${ns}`).toEqual(flattenKeys(en).sort());
		}
	});
});