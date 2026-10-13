import { vi } from "vitest";
import common from "@/lib/i18n/locales/en/common.json";
import logs from "@/lib/i18n/locales/en/logs.json";
import mcp from "@/lib/i18n/locales/en/mcp.json";

/**
 * Test-only i18n wiring: components under test call useTranslation()/i18n.t(),
 * and t() resolves against the real en locale JSON so assertions read the exact
 * English copy, with {{interpolation}} applied.
 *
 * Usage — import and call at the top level of a test file:
 *   import { mockI18n } from "@/lib/i18n/testI18n";
 *   mockI18n();
 * The mock must cover every namespace the component renders (common, logs, mcp).
 */

const RESOURCES: Record<string, unknown> = { common, logs, mcp };

function lookup(ns: string, key: string): string {
	const value = key.split(".").reduce<unknown>((node, part) => {
		if (node && typeof node === "object" && part in (node as Record<string, unknown>)) {
			return (node as Record<string, unknown>)[part];
		}
		return undefined;
	}, RESOURCES[ns]);
	return typeof value === "string" ? value : key;
}

function interpolate(template: string, opts?: Record<string, unknown>): string {
	return template.replace(/\{\{(\w+)\}\}/g, (_, name: string) => String(opts?.[name] ?? `{{${name}}}`));
}

export function translate(key: string, opts?: Record<string, unknown>): string {
	const separator = key.indexOf(":");
	const ns = separator === -1 ? "common" : key.slice(0, separator);
	const rest = separator === -1 ? key : key.slice(separator + 1);
	return interpolate(lookup(ns, rest), opts);
}

// vi.mock calls are hoisted above imports; the factories close over translate,
// which is resolved lazily when the mocked functions are invoked.
vi.mock("react-i18next", () => ({
	useTranslation: () => ({ t: translate }),
}));
vi.mock("@/lib/i18n", () => ({
	default: { t: translate },
	changeLanguage: vi.fn(),
}));

/** Enables the i18n mocks above. Must be called at the test file's top level. */
export function mockI18n(): void {}
