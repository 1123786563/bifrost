import { Button } from "@/components/ui/button";
import { ArrowUpRight, SquareTerminal } from "lucide-react";
import { usePromptContext } from "../context";
import { useTranslation } from "react-i18next";

export function EmptyState() {
	const { t } = useTranslation(["common", "workspaces"]);
	const { setPromptSheet, canCreate } = usePromptContext();

	return (
		<div className="text-muted-foreground flex h-full items-center justify-center">
			<div className="text-center">
				<p className="text-lg font-medium">{t("workspaces:prompts.noneSelected")}</p>
				<p className="text-sm">
					{canCreate ? (
						<>
							{t("workspaces:promptsRepo.empty.selectOrCreate")}{" "}
							<Button
								variant="link"
								className="h-auto p-0 text-sm"
								data-testid="empty-state-create-prompt-link"
								onClick={() => setPromptSheet({ open: true })}
							>
								{t("workspaces:promptsRepo.empty.createNew")}
							</Button>
						</>
					) : (
						t("workspaces:promptsRepo.empty.noPromptSelected")
					)}
				</p>
			</div>
		</div>
	);
}

export function PromptsEmptyState() {
	const { t } = useTranslation(["common", "workspaces"]);
	const { setPromptSheet, canCreate } = usePromptContext();

	return (
		<div className="flex min-h-[80vh] w-full flex-col items-center justify-center gap-4 py-16 text-center">
			<div className="text-muted-foreground">
				<SquareTerminal className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">{t("workspaces:promptsRepo.empty.title")}</h1>
				<div className="text-muted-foreground mx-auto mt-2 w-full max-w-[600px] text-sm font-normal">
					{canCreate ? t("workspaces:promptsRepo.empty.canCreateDesc") : t("workspaces:promptsRepo.empty.viewOnlyDesc")}
				</div>
				<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
					<Button
						variant="outline"
						aria-label={t("workspaces:promptsRepo.empty.readMoreAria")}
						data-testid="empty-state-read-more"
						onClick={() => {
							window.open(`https://docs.getbifrost.ai/features/prompt-repository?utm_source=bfd`, "_blank", "noopener,noreferrer");
						}}
					>
						{t("workspaces:promptsRepo.empty.readMore")} <ArrowUpRight className="text-muted-foreground h-3 w-3" />
					</Button>
					{canCreate && (
						<Button
							aria-label={t("workspaces:promptsRepo.empty.createFirstAria")}
							data-testid="empty-state-create-prompt"
							onClick={() => setPromptSheet({ open: true })}
						>
							{t("workspaces:promptsRepo.empty.createPrompt")}
						</Button>
					)}
				</div>
			</div>
		</div>
	);
}