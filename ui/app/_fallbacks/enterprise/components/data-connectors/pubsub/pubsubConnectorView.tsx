import { Radio } from "lucide-react";
import ContactUsView from "../../views/contactUsView";
import { useTranslation } from "react-i18next";

interface EnableToggleProps {
	enabled: boolean;
	onToggle: () => void;
	disabled?: boolean;
}

interface PubSubConnectorViewProps {
	onDelete?: () => void;
	isDeleting?: boolean;
	enableToggle?: EnableToggleProps;
}

export default function PubSubConnectorView(_props: PubSubConnectorViewProps) {
	const { t } = useTranslation(["errors"]);
	return (
		<div className="space-y-6">
			<div className="space-y-4">
				<div className="flex w-full flex-col items-center justify-center py-8">
					<ContactUsView
						align="middle"
						className="mx-auto w-full max-w-lg"
						icon={<Radio className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
						title={t("errors:upsell.pubsub.title")}
						description={t("errors:upsell.pubsub.description")}
						readmeLink="https://docs.getbifrost.ai/enterprise/pubsub-connector"
						testIdPrefix="pubsub-connector"
					/>
				</div>
			</div>
		</div>
	);
}