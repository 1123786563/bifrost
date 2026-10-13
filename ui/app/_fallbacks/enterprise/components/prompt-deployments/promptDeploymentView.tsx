import { Router } from "lucide-react";
import ContactUsView from "../views/contactUsView";
import { useTranslation } from "react-i18next";

export default function PromptDeploymentView(_props?: { omitTitle?: boolean }) {
	const { t } = useTranslation(["errors"]);
	return (
		<div className="w-full">
			<ContactUsView
				align="top"
				className="justify-start gap-3 rounded-md border p-4"
				icon={<Router className="h-8 w-8" strokeWidth={1.5} />}
				title={t("errors:upsell.promptDeployments.title")}
				description={t("errors:upsell.promptDeployments.description")}
				readmeLink="https://docs.getbifrost.ai/enterprise/prompt-deployments"
			/>
		</div>
	);
}