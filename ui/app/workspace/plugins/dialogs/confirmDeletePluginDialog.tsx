import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
} from "@/components/ui/alertDialog";
import { getErrorMessage, useDeletePluginMutation } from "@/lib/store";
import { Plugin } from "@/lib/types/plugins";
import { AlertDialogTitle } from "@radix-ui/react-alert-dialog";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

interface Props {
	show: boolean;
	onCancel: () => void;
	onDelete: () => void;
	plugin: Plugin;
}

export default function ConfirmDeletePluginDialog({ show, onCancel, onDelete, plugin }: Props) {
	const { t } = useTranslation(["common", "plugins"]);
	const [deletePlugin, { isLoading: isDeletingPlugin }] = useDeletePluginMutation();

	const onDeleteHandler = () => {
		deletePlugin(plugin.name)
			.unwrap()
			.then(() => {
				onDelete();
			})
			.catch((err) => {
				toast.error(t("plugins:messages.deleteFailed"), {
					description: getErrorMessage(err),
				});
			});
	};

	return (
		<AlertDialog open={show}>
			<AlertDialogContent>
				<AlertDialogHeader>
					<AlertDialogTitle>{t("plugins:dialog.deleteTitle")}</AlertDialogTitle>
					<AlertDialogDescription>{t("plugins:dialog.confirmBody", { name: plugin.name })}</AlertDialogDescription>
				</AlertDialogHeader>
				<AlertDialogFooter>
					<AlertDialogCancel onClick={onCancel}>{t("common:actions.cancel")}</AlertDialogCancel>
					<AlertDialogAction onClick={onDeleteHandler} disabled={isDeletingPlugin}>
						{isDeletingPlugin ? t("plugins:dialog.deleting") : t("common:actions.delete")}
					</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}