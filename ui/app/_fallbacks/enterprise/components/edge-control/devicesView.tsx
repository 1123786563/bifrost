import { MonitorSmartphone } from "lucide-react";
import EdgeControlFallbackView from "./fallbackWrapper";
import { useTranslation } from "react-i18next";

export default function DevicesView() {
	const { t } = useTranslation(["errors"]);
	return (
		<EdgeControlFallbackView
			icon={<MonitorSmartphone className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
			title={t("errors:upsell.edgeControlDevices.title")}
			description={t("errors:upsell.edgeControlDevices.description")}
			readmeLink="https://docs.getbifrost.ai/edge/admin-devices"
			testIdPrefix="edge-devices"
		/>
	);
}