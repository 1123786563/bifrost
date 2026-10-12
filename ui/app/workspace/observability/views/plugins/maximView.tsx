import { getErrorMessage, useAppSelector, useUpdatePluginMutation } from "@/lib/store";
import { useTranslation } from "react-i18next";
import { MaximConfigSchema, MaximFormSchema } from "@/lib/types/schemas";
import { useMemo } from "react";
import { toast } from "sonner";
import { MaximFormFragment } from "../../fragments/maximFormFragment";

interface MaximViewProps {
	onDelete?: () => void;
	isDeleting?: boolean;
}

export default function MaximView({ onDelete, isDeleting }: MaximViewProps) {
	const { t } = useTranslation(["logs"]);
	const selectedPlugin = useAppSelector((state) => state.plugin.selectedPlugin);
	const [updatePlugin] = useUpdatePluginMutation();
	const currentConfig = useMemo(
		() => ({ ...((selectedPlugin?.config as MaximConfigSchema) ?? {}), enabled: selectedPlugin?.enabled }),
		[selectedPlugin],
	);

	const handleMaximConfigSave = (config: MaximFormSchema): Promise<void> => {
		return new Promise((resolve, reject) => {
			updatePlugin({
				name: "maxim",
				data: {
					enabled: config.enabled,
					config: config.maxim_config,
				},
			})
				.unwrap()
				.then(() => {
					toast.success(t("logs:maxim.updatedSuccess"));
					resolve();
				})
				.catch((err) => {
					toast.error(t("logs:maxim.updateFailed"), {
						description: getErrorMessage(err),
					});
					reject(err);
				});
		});
	};

	return (
		<div className="flex w-full flex-col gap-4">
			<div className="flex w-full flex-col gap-2">
				<div className="text-muted-foreground text-xs font-medium">{t("logs:observability.configuration")}</div>
				<div className="text-muted-foreground mb-2 text-xs font-normal">{t("logs:maxim.repoHeaderNote")}</div>
				<MaximFormFragment onSave={handleMaximConfigSave} initialConfig={currentConfig} onDelete={onDelete} isDeleting={isDeleting} />
			</div>
		</div>
	);
}