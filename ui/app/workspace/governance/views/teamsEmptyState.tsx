import { Button } from "@/components/ui/button";
import { DisabledReason } from "@/components/ui/disabledReason";
import { actionDisabledReason } from "@/lib/utils/governance";
import { Building } from "lucide-react";
import { ArrowUpRight } from "lucide-react";
import { useTranslation } from "react-i18next";

const TEAMS_DOCS_URL = "https://docs.getbifrost.ai/features/governance/virtual-keys#teams";

interface TeamsEmptyStateProps {
	onAddClick: () => void;
	canCreate?: boolean;
}

export function TeamsEmptyState({ onAddClick, canCreate = true }: TeamsEmptyStateProps) {
	const { t } = useTranslation(["common", "governance"]);
	return (
		<div className="flex min-h-[80vh] w-full flex-col items-center justify-center gap-4 py-16 text-center">
			<div className="text-muted-foreground">
				<Building className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">{t("governance:org.teams.emptyTitle")}</h1>
				<div className="text-muted-foreground mx-auto mt-2 w-full max-w-[600px] text-sm font-normal">
					{t("governance:org.teams.emptyDescription")}
				</div>
				<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
					<Button
						variant="outline"
						aria-label={t("governance:org.teams.readMoreAria")}
						data-testid="team-button-read-more"
						onClick={() => {
							window.open(`${TEAMS_DOCS_URL}?utm_source=bfd`, "_blank", "noopener,noreferrer");
						}}
					>
						{t("governance:org.teams.readMore")} <ArrowUpRight className="text-muted-foreground h-3 w-3" />
					</Button>
					<DisabledReason reason={actionDisabledReason(canCreate, "create", "teams")}>
						<Button onClick={onAddClick} disabled={!canCreate} data-testid="team-button-add">
							{t("governance:org.teams.add")}
						</Button>
					</DisabledReason>
				</div>
			</div>
		</div>
	);
}