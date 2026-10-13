import AlertingPlaceholderView from "./alertingPlaceholderView";
import { useTranslation } from "react-i18next";

export default function AlertRulesView() {
	const { t } = useTranslation(["errors"]);
	return (
		<AlertingPlaceholderView
			title={t("errors:upsell.alertingRules.title")}
			description={t("errors:upsell.alertingRules.description")}
			readmeLink="https://docs.getbifrost.ai/enterprise/alerting/alert-rules"
			testIdPrefix="alert-rules"
		/>
	);
}