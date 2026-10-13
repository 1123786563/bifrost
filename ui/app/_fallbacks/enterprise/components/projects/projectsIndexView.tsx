import { FolderKanban } from "lucide-react";
import ContactUsView from "../views/contactUsView";
import { useTranslation } from "react-i18next";

export default function ProjectsIndexView() {
	const { t } = useTranslation(["errors"]);
	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<FolderKanban className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title={t("errors:upsell.projects.title")}
				description={t("errors:upsell.projects.description")}
				readmeLink="https://docs.getbifrost.ai/enterprise/projects"
				testIdPrefix="projects"
			/>
		</div>
	);
}