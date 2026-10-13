import { Users } from "lucide-react";
import ContactUsView from "../views/contactUsView";
import { useTranslation } from "react-i18next";

export default function UserRankingsTab() {
	const { t } = useTranslation(["errors"]);
	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<Users className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title={t("errors:upsell.userRankings.title")}
				description={t("errors:upsell.userRankings.description")}
				readmeLink="https://docs.getbifrost.ai/enterprise/user-rankings"
				testIdPrefix="user-rankings"
			/>
		</div>
	);
}