import { ShieldCheck } from "lucide-react";
import EdgeControlFallbackView from "./fallbackWrapper";
import { useTranslation } from "react-i18next";

export default function InventoryView() {
	const { t } = useTranslation(["errors"]);
	return (
		<EdgeControlFallbackView
			icon={<ShieldCheck className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
			title={t("errors:upsell.edgeControlInventory.title")}
			description={t("errors:upsell.edgeControlInventory.description")}
			readmeLink="https://docs.getbifrost.ai/edge/admin-approvals"
			testIdPrefix="edge-inventory"
		/>
	);
}