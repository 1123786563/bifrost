import AlertingPlaceholderView from "./alertingPlaceholderView";
import { useTranslation } from "react-i18next";

export default function AlertChannelsView() {
	const { t } = useTranslation(["errors"]);
	return (
		<AlertingPlaceholderView
			title={t("errors:upsell.alertingChannels.title")}
			description={t("errors:upsell.alertingChannels.description")}
			readmeLink="https://docs.getbifrost.ai/enterprise/alerting/alert-channels"
			testIdPrefix="alert-channels"
		/>
	);
}