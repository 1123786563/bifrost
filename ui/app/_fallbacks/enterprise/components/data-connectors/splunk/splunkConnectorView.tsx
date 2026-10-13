import { Database } from "lucide-react";
import ContactUsView from "../../views/contactUsView";
import { useTranslation } from "react-i18next";

interface EnableToggleProps {
	enabled: boolean;
	onToggle: () => void;
	disabled?: boolean;
}

interface SplunkConnectorViewProps {
	onDelete?: () => void;
	isDeleting?: boolean;
	enableToggle?: EnableToggleProps;
}

export default function SplunkConnectorView(_props: SplunkConnectorViewProps) {
	const { t } = useTranslation(["errors"]);
	return (
		<div className="space-y-6">
			{/* Content - OSS: paywall only; no delete/save buttons */}
			<div className="space-y-4">
				<div className="flex w-full flex-col items-center justify-center py-8">
					<ContactUsView
						align="middle"
						className="mx-auto w-full max-w-lg"
						icon={<Database className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
						title={t("errors:upsell.splunk.title")}
						description={t("errors:upsell.splunk.description")}
						readmeLink="https://docs.getbifrost.ai/features/observability/splunk"
					/>
				</div>
			</div>
		</div>
	);
}