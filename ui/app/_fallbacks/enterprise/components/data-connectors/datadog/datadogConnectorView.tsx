import { Dog } from "lucide-react";
import ContactUsView from "../../views/contactUsView";
import { useTranslation } from "react-i18next";

interface EnableToggleProps {
	enabled: boolean;
	onToggle: () => void;
	disabled?: boolean;
}

interface DatadogConnectorViewProps {
	onDelete?: () => void;
	isDeleting?: boolean;
	enableToggle?: EnableToggleProps;
}

export default function DatadogConnectorView(_props: DatadogConnectorViewProps) {
	const { t } = useTranslation(["errors"]);
	return (
		<div className="space-y-6">
			{/* Content - OSS: paywall only; no delete/save buttons */}
			<div className="space-y-4">
				<div className="flex w-full flex-col items-center justify-center py-8">
					<ContactUsView
						align="middle"
						className="mx-auto w-full max-w-lg"
						icon={<Dog className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
						title={t("errors:upsell.datadog.title")}
						description={t("errors:upsell.datadog.description")}
						readmeLink="https://docs.getbifrost.ai/enterprise/datadog-connector"
					/>
				</div>
			</div>
		</div>
	);
}