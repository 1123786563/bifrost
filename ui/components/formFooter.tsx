import { Button } from "@/components/ui/button";
import { DialogFooter } from "@/components/ui/dialog";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { Validator } from "@/lib/utils/validation";
import { Save } from "lucide-react";
import { useTranslation } from "react-i18next";

interface FormFooterProps {
	validator: Validator;
	label: string;
	onCancel: () => void;
	isLoading: boolean;
	isEditing: boolean;
	hasPermission?: boolean;
}

export default function FormFooter({ validator, label, onCancel, isLoading, isEditing, hasPermission = true }: FormFooterProps) {
	const { t } = useTranslation(["common", "workspaces"]);
	const isDisabled = isLoading || !validator.isValid() || !hasPermission;

	const getTooltipMessage = () => {
		if (!hasPermission) return t("workspaces:formFooter.noPermission");
		if (isLoading) return t("workspaces:formFooter.saving");
		return validator.getFirstError() || t("workspaces:formFooter.fixErrors");
	};

	return (
		<DialogFooter className="mt-4">
			<Button type="button" variant="outline" onClick={onCancel} disabled={isLoading}>
				{t("common:actions.cancel")}
			</Button>
			<TooltipProvider>
				<Tooltip>
					<TooltipTrigger asChild>
						<span>
							<Button type="submit" disabled={isDisabled}>
								<Save className="h-4 w-4" />
								{isLoading
									? t("workspaces:formFooter.saving")
									: isEditing
										? t("workspaces:formFooter.update", { label })
										: t("workspaces:formFooter.create", { label })}
							</Button>
						</span>
					</TooltipTrigger>
					{isDisabled && (
						<TooltipContent>
							<p>{getTooltipMessage()}</p>
						</TooltipContent>
					)}
				</Tooltip>
			</TooltipProvider>
		</DialogFooter>
	);
}