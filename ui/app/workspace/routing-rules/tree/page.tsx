/**
 * Routing Tree Page
 * Full-canvas read-only routing rules decision tree visualizer.
 */

import i18n from "@/lib/i18n";
import { RoutingTreeView } from "./views/routingTreeView";

// Labels resolve at access time through the governance ns the tree route already loads;
// defaultValue keeps the export readable before i18n initializes (pricingFields.ts pattern).
export const metadata = {
	get title() {
		return i18n.t("governance:routingRules.tree.title", { defaultValue: "Routing Tree | Bifrost" });
	},
	get description() {
		return i18n.t("governance:routingRules.tree.description", {
			defaultValue: "Read-only decision tree visualization of routing rules",
		});
	},
};

export default function RoutingTreePage() {
	return (
		<div className="no-padding-parent no-border-parent h-[calc(var(--app-content-viewport)_)] w-full">
			<RoutingTreeView />
		</div>
	);
}