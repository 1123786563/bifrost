import { UserRoundCheck } from "lucide-react";
import ContactUsView from "../views/contactUsView";
import { useTranslation } from "react-i18next";

export default function RBACView() {
	const { t } = useTranslation(["errors"]);
	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<UserRoundCheck className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title={t("errors:upsell.rbac.title")}
				description={t("errors:upsell.rbac.description")}
				readmeLink="https://docs.getbifrost.ai/enterprise/advanced-governance"
			/>
		</div>
	);
}