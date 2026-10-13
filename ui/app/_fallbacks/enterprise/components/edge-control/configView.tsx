import { SlidersHorizontal } from "lucide-react";
import EdgeControlFallbackView from "./fallbackWrapper";
import { useTranslation } from "react-i18next";

export default function ConfigView() {
	const { t } = useTranslation(["errors"]);
	return (
		<EdgeControlFallbackView
			icon={<SlidersHorizontal className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
			title={t("errors:upsell.edgeControlConfig.title")}
			description={t("errors:upsell.edgeControlConfig.description")}
			readmeLink="https://docs.getbifrost.ai/edge/admin-configurations"
			testIdPrefix="edge-config"
		/>
	);
}