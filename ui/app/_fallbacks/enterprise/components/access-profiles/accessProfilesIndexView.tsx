import { ShieldCheck } from "lucide-react";
import ContactUsView from "../views/contactUsView";
import { useTranslation } from "react-i18next";

export default function AccessProfilesIndexView() {
	const { t } = useTranslation(["errors"]);
	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<ShieldCheck className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title={t("errors:upsell.accessProfiles.title")}
				description={t("errors:upsell.accessProfiles.description")}
				readmeLink="https://docs.getbifrost.ai/enterprise/access-profiles"
				testIdPrefix="access-profiles"
			/>
		</div>
	);
}