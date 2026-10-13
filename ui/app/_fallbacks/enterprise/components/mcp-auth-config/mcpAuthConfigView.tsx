import { ShieldUser } from "lucide-react";
import ContactUsView from "../views/contactUsView";
import { useTranslation } from "react-i18next";

export default function MCPAuthConfigView() {
	const { t } = useTranslation(["errors"]);
	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<ShieldUser className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title={t("errors:upsell.mcpAuthConfig.title")}
				description={t("errors:upsell.mcpAuthConfig.description")}
				readmeLink="https://docs.getbifrost.ai/mcp/overview"
			/>
		</div>
	);
}