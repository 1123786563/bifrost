import { translate } from "@/lib/i18n/testI18n";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { useOnboardingChecklist } from "./useOnboardingChecklist";

// The checklist's step ids and section keys are derived without touching
// query data, so the store hooks are stubbed out and the hook runs outside a
// Redux provider. IS_ENTERPRISE is forced on so the enterprise-only steps
// (scim, models, virtual-keys) are part of the locale contract too.
vi.mock("@/lib/constants/config", () => ({ IS_ENTERPRISE: true }));
vi.mock("@/lib/store", () => ({ useGetCoreConfigQuery: () => ({ data: undefined }) }));
vi.mock("@/lib/store/apis/governanceApi", () => ({
	useGetModelConfigsQuery: () => ({ data: undefined }),
	useGetVirtualKeysQuery: () => ({ data: undefined }),
}));
vi.mock("@/lib/store/apis/providersApi", () => ({ useGetAllKeysQuery: () => ({ data: undefined }) }));
vi.mock("@enterprise/lib/store/apis/scimApi", () => ({ useGetSCIMProvidersQuery: () => ({ data: undefined }) }));

interface ProbeStep {
	id: string;
	title: string;
	section: string;
}

function ChecklistProbe() {
	const { steps } = useOnboardingChecklist();
	const payload: ProbeStep[] = steps.map((s) => ({ id: s.id, title: s.title, section: s.section }));
	// data-probe survives renderToStaticMarkup with HTML-escaped JSON inside.
	return <div data-probe={JSON.stringify(payload)} />;
}

function renderSteps(): ProbeStep[] {
	const markup = renderToStaticMarkup(<ChecklistProbe />);
	const match = markup.match(/data-probe="([^"]*)"/);
	if (!match) throw new Error("probe attribute missing from markup");
	const json = match[1].replace(/&quot;/g, '"').replace(/&amp;/g, "&");
	return JSON.parse(json) as ProbeStep[];
}

/** translate() returns the namespace-stripped key path on a miss, so "not the stripped path" is "resolved". */
function expectResolves(key: string) {
	const resolved = translate(key);
	const missPath = key.slice(key.indexOf(":") + 1);
	expect(resolved, `missing locale key "${key}"`).not.toBe(missPath);
}

describe("useOnboardingChecklist locale contract", () => {
	it("exposes the seven known step ids", () => {
		expect(renderSteps().map((s) => s.id).sort()).toEqual(
			["cors", "dashboard-auth", "enforce-inference-auth", "models", "provider-key", "scim", "virtual-keys"].sort(),
		);
	});

	it("every step id resolves a workspaces:onboarding.steps.<id> entry", () => {
		for (const step of renderSteps()) {
			expectResolves(`workspaces:onboarding.steps.${step.id}`);
		}
	});

	it("sections are stable keys that resolve workspaces:onboarding.sections.<key>", () => {
		const sections = [...new Set(renderSteps().map((s) => s.section))];
		expect(sections.sort()).toEqual(["everythingElse", "providerSetup", "security"].sort());
		for (const section of sections) {
			expectResolves(`workspaces:onboarding.sections.${section}`);
		}
	});

	it("keeps a non-empty English title per step as render-time defaultValue fallback", () => {
		for (const step of renderSteps()) {
			expect(step.title.length).toBeGreaterThan(0);
		}
	});

	it("widget chrome strings resolve locale entries", () => {
		expectResolves("workspaces:onboarding.minimizedLabel");
		expectResolves("workspaces:onboarding.pickDate");
		expectResolves("workspaces:onboarding.hideForAll");
	});
});
