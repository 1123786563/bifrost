import { Button } from "@/components/ui/button";
import { DisabledReason } from "@/components/ui/disabledReason";
import { actionDisabledReason } from "@/lib/utils/governance";
import { Wallet } from "lucide-react";
import { ArrowUpRight } from "lucide-react";
import { useTranslation } from "react-i18next";

const MODEL_LIMITS_DOCS_URL = "https://docs.getbifrost.ai/features/governance";

interface ModelLimitsEmptyStateProps {
	onAddClick: () => void;
	canCreate?: boolean;
}

export function ModelLimitsEmptyState({ onAddClick, canCreate = true }: ModelLimitsEmptyStateProps) {
	const { t } = useTranslation(["common", "models"]);
	return (
		<div className="flex min-h-[80vh] w-full flex-col items-center justify-center gap-4 py-16 text-center">
			<div className="text-muted-foreground">
				<Wallet className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">{t("models:limits.emptyState.title")}</h1>
				<div className="text-muted-foreground mx-auto mt-2 w-full max-w-[600px] text-sm font-normal">
					{t("models:limits.emptyState.description")}
				</div>
				<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
					<Button
						variant="outline"
						aria-label={t("models:limits.emptyState.readMoreAria")}
						data-testid="model-limits-button-read-more"
						onClick={() => {
							window.open(`${MODEL_LIMITS_DOCS_URL}?utm_source=bfd`, "_blank", "noopener,noreferrer");
						}}
					>
						{t("models:limits.emptyState.readMore")} <ArrowUpRight className="text-muted-foreground h-3 w-3" />
					</Button>
					<DisabledReason reason={actionDisabledReason(canCreate, "create", "modelLimits")}>
						<Button
							aria-label={t("models:limits.emptyState.addLimitAria")}
							onClick={onAddClick}
							disabled={!canCreate}
							data-testid="model-limits-button-create"
						>
							{t("models:limits.emptyState.addLimit")}
						</Button>
					</DisabledReason>
				</div>
			</div>
		</div>
	);
}