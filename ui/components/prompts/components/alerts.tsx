import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
} from "@/components/ui/alertDialog";
import { usePromptContext } from "../context";
import { useTranslation } from "react-i18next";

export function DeleteFolderDialog() {
	const { t } = useTranslation(["common", "workspaces"]);
	const { deleteFolderDialog, setDeleteFolderDialog, isDeletingFolder, handleDeleteFolder } = usePromptContext();

	return (
		<AlertDialog open={deleteFolderDialog.open}>
			<AlertDialogContent>
				<AlertDialogHeader>
					<AlertDialogTitle>{t("workspaces:promptsRepo.alerts.deleteFolderTitle")}</AlertDialogTitle>
					<AlertDialogDescription>
						{t("workspaces:promptsRepo.alerts.deleteFolderBody", { name: deleteFolderDialog.folder?.name ?? "" })}
					</AlertDialogDescription>
				</AlertDialogHeader>
				<AlertDialogFooter>
					<AlertDialogCancel
						data-testid="delete-folder-cancel"
						onClick={() => setDeleteFolderDialog({ open: false })}
						disabled={isDeletingFolder}
					>
						{t("common:actions.cancel")}
					</AlertDialogCancel>
					<AlertDialogAction data-testid="delete-folder-confirm" onClick={handleDeleteFolder} disabled={isDeletingFolder}>
						{isDeletingFolder ? t("workspaces:promptsRepo.alerts.deleting") : t("common:actions.delete")}
					</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}

export function DeletePromptDialog() {
	const { t } = useTranslation(["common", "workspaces"]);
	const { deletePromptDialog, setDeletePromptDialog, isDeletingPrompt, handleDeletePrompt } = usePromptContext();

	return (
		<AlertDialog open={deletePromptDialog.open}>
			<AlertDialogContent>
				<AlertDialogHeader>
					<AlertDialogTitle>{t("workspaces:promptsRepo.alerts.deletePromptTitle")}</AlertDialogTitle>
					<AlertDialogDescription>
						{t("workspaces:promptsRepo.alerts.deletePromptBody", { name: deletePromptDialog.prompt?.name ?? "" })}
					</AlertDialogDescription>
				</AlertDialogHeader>
				<AlertDialogFooter>
					<AlertDialogCancel
						data-testid="delete-prompt-cancel"
						onClick={() => setDeletePromptDialog({ open: false })}
						disabled={isDeletingPrompt}
					>
						{t("common:actions.cancel")}
					</AlertDialogCancel>
					<AlertDialogAction data-testid="delete-prompt-confirm" onClick={handleDeletePrompt} disabled={isDeletingPrompt}>
						{isDeletingPrompt ? t("workspaces:promptsRepo.alerts.deleting") : t("common:actions.delete")}
					</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}