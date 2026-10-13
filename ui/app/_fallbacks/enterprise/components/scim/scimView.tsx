import { BookUser } from "lucide-react";
import ContactUsView from "../views/contactUsView";
import { useTranslation } from "react-i18next";

export default function SCIMView() {
	const { t } = useTranslation(["errors"]);
	// Same bordered panel the other enterprise placeholders use, so the upsell
	// reads as page content rather than as the page background.
	return (
		<div className="rounded-sm border">
			<div className="flex w-full flex-col items-center justify-center py-16">
				<ContactUsView
					className="mx-auto w-full max-w-lg"
					icon={<BookUser className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
					title={t("errors:upsell.scim.title")}
					description={t("errors:upsell.scim.description")}
					readmeLink="https://docs.getbifrost.ai/enterprise/advanced-governance"
				/>
			</div>
		</div>
	);
}