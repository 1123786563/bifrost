import { KeyRound } from "lucide-react";
import ContactUsView from "../views/contactUsView";
import { useTranslation } from "react-i18next";

export default function LicenseSettingsView() {
	const { t } = useTranslation(["errors"]);
	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<KeyRound className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title={t("errors:upsell.license.title")}
				description={t("errors:upsell.license.description")}
				readmeLink="https://docs.getbifrost.ai/enterprise/overview"
				testIdPrefix="license"
			/>
		</div>
	);
}