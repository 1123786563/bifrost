import AlertingPlaceholderView from "./alertingPlaceholderView";
import { useTranslation } from "react-i18next";

export default function AlertHistoryView() {
	const { t } = useTranslation(["errors"]);
	return (
		<AlertingPlaceholderView
			title={t("errors:upsell.alertingHistory.title")}
			description={t("errors:upsell.alertingHistory.description")}
			readmeLink="https://docs.getbifrost.ai/enterprise/alerting/alert-history"
			testIdPrefix="alert-history"
		/>
	);
}