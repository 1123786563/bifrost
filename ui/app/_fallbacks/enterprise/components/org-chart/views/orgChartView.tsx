import { Network } from "lucide-react";
import ContactUsView from "../../views/contactUsView";
import { useTranslation } from "react-i18next";

export function OrgChartView() {
	const { t } = useTranslation(["errors"]);
	return (
		<div className="w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				testIdPrefix="org-chart"
				icon={<Network className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title={t("errors:upsell.orgChart.title")}
				description={t("errors:upsell.orgChart.description")}
				readmeLink="https://docs.getbifrost.ai/enterprise/advanced-governance"
			/>
		</div>
	);
}