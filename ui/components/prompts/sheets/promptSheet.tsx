import { Button } from "@/components/ui/button";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib/contexts/rbacContext";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { getErrorMessage } from "@/lib/store";
import { useCreatePromptMutation, useUpdatePromptMutation } from "@/lib/store/apis/promptsApi";
import { Prompt } from "@/lib/types/prompts";
import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { useTranslation } from "react-i18next";

interface PromptFormData {
	name: string;
}

interface PromptSheetProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	prompt?: Prompt;
	folderId?: string;
	onSaved: (promptId?: string) => void;
}

export function PromptSheet({ open, onOpenChange, prompt, folderId, onSaved }: PromptSheetProps) {
	const { t } = useTranslation(["common", "workspaces"]);
	const [createPrompt, { isLoading: isCreating }] = useCreatePromptMutation();
	const [updatePrompt, { isLoading: isUpdating }] = useUpdatePromptMutation();

	const isLoading = isCreating || isUpdating;
	const isEditing = !!prompt;
	const canSave = useRbac(RbacResource.PromptRepository, isEditing ? RbacOperation.Update : RbacOperation.Create);

	const {
		register,
		handleSubmit,
		reset,
		formState: { errors },
	} = useForm<PromptFormData>({
		defaultValues: { name: "" },
	});

	useEffect(() => {
		if (open) {
			reset({ name: prompt?.name ?? "" });
		}
	}, [open, prompt, reset]);

	async function onSubmit(data: PromptFormData) {
		try {
			if (isEditing) {
				await updatePrompt({
					id: prompt.id,
					data: { name: data.name.trim() },
				}).unwrap();
				toast.success(t("workspaces:promptsRepo.promptSheet.updated"));
				onSaved();
			} else {
				const result = await createPrompt({
					name: data.name.trim(),
					...(folderId ? { folder_id: folderId } : {}),
				}).unwrap();
				toast.success(t("workspaces:promptsRepo.promptSheet.created"));
				onSaved(result.prompt.id);
			}
			onOpenChange(false);
		} catch (err) {
			toast.error(`Failed to ${isEditing ? "update" : "create"} prompt`, {
				description: getErrorMessage(err),
			});
		}
	}

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent
				className="p-0"
				onOpenAutoFocus={(e) => {
					e.preventDefault();
					document.getElementById("name")?.focus();
				}}
			>
				<form onSubmit={handleSubmit(onSubmit)} className="flex grow flex-col">
					<SheetHeader className="flex flex-col items-start pt-8" headerClassName="px-4 md:px-8">
						<SheetTitle>{isEditing ? "Rename Prompt" : "Create Prompt"}</SheetTitle>
						<SheetDescription>
							{isEditing
								? t("workspaces:promptsRepo.promptSheet.editDesc")
								: folderId
									? t("workspaces:promptsRepo.promptSheet.createInFolderDesc")
									: t("workspaces:promptsRepo.promptSheet.createDesc")}
						</SheetDescription>
					</SheetHeader>

					<div className="flex grow flex-col gap-6">
						<div className="grow space-y-4 px-4 md:px-8">
							<div className="space-y-2">
								<Label htmlFor="name">{t("common:fields.name")}</Label>
								<Input
									id="name"
									data-testid="prompt-name-input"
									placeholder={t("workspaces:promptsRepo.promptSheet.namePh")}
									{...register("name", {
										required: t("workspaces:promptsRepo.promptSheet.nameRequired"),
										validate: (v) => v.trim().length > 0 || t("workspaces:promptsRepo.promptSheet.nameBlank"),
									})}
									autoFocus
								/>
								{errors.name && <p className="text-destructive text-xs">{errors.name.message}</p>}
							</div>
						</div>

						<SheetFooter className="flex flex-row items-center justify-end gap-2 border-t px-4 py-4 md:px-8">
							<Button type="button" variant="outline" data-testid="prompt-cancel" onClick={() => onOpenChange(false)}>
								Cancel
							</Button>
							<Button
								type="submit"
								data-testid="prompt-submit"
								disabled={isLoading || !canSave}
								title={canSave ? undefined : t("workspaces:promptsRepo.promptSheet.noPermission")}
							>
								{isLoading ? "Saving..." : isEditing ? "Update" : "Create"}
							</Button>
						</SheetFooter>
					</div>
				</form>
			</SheetContent>
		</Sheet>
	);
}