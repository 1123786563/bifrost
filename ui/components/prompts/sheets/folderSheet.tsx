import { Button } from "@/components/ui/button";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib/contexts/rbacContext";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Textarea } from "@/components/ui/textarea";
import { getErrorMessage } from "@/lib/store";
import { useCreateFolderMutation, useUpdateFolderMutation } from "@/lib/store/apis/promptsApi";
import { Folder } from "@/lib/types/prompts";
import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { useTranslation } from "react-i18next";

interface FolderFormData {
	name: string;
	description: string;
}

interface FolderSheetProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	folder?: Folder;
	onSaved: () => void;
}

export function FolderSheet({ open, onOpenChange, folder, onSaved }: FolderSheetProps) {
	const { t } = useTranslation(["common", "workspaces"]);
	const [createFolder, { isLoading: isCreating }] = useCreateFolderMutation();
	const [updateFolder, { isLoading: isUpdating }] = useUpdateFolderMutation();
	const canSave = useRbac(RbacResource.PromptRepository, folder ? RbacOperation.Update : RbacOperation.Create);

	const isLoading = isCreating || isUpdating;
	const isEditing = !!folder;

	const {
		register,
		handleSubmit,
		reset,
		formState: { errors },
	} = useForm<FolderFormData>({
		defaultValues: { name: "", description: "" },
	});

	useEffect(() => {
		if (open) {
			reset({
				name: folder?.name ?? "",
				description: folder?.description ?? "",
			});
		}
	}, [open, folder, reset]);

	async function onSubmit(data: FolderFormData) {
		try {
			if (isEditing) {
				await updateFolder({
					id: folder.id,
					data: { name: data.name.trim(), description: data.description.trim() || undefined },
				}).unwrap();
				toast.success(t("workspaces:promptsRepo.folderSheet.updated"));
			} else {
				await createFolder({
					name: data.name.trim(),
					description: data.description.trim() || undefined,
				}).unwrap();
				toast.success(t("workspaces:promptsRepo.folderSheet.created"));
			}
			onSaved();
			onOpenChange(false);
		} catch (err) {
			toast.error(t(isEditing ? "workspaces:promptsRepo.folderSheet.updateFailed" : "workspaces:promptsRepo.folderSheet.createFailed"), {
				description: getErrorMessage(err),
			});
		}
	}

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent
				className="p-4 md:p-8"
				onOpenAutoFocus={(e) => {
					e.preventDefault();
					document.getElementById("name")?.focus();
				}}
			>
				<form onSubmit={handleSubmit(onSubmit)}>
					<SheetHeader className="flex flex-col items-start">
						<SheetTitle>{isEditing ? t("workspaces:prompts.editFolder") : t("workspaces:promptsRepo.folderSheet.createTitle")}</SheetTitle>
						<SheetDescription>
							{isEditing ? t("workspaces:promptsRepo.folderSheet.editDesc") : t("workspaces:promptsRepo.folderSheet.createDesc")}
						</SheetDescription>
					</SheetHeader>

					<div className="mt-6 space-y-4">
						<div className="space-y-2">
							<Label htmlFor="name">{t("common:fields.name")}</Label>
							<Input
								id="name"
								data-testid="folder-name-input"
								placeholder={t("workspaces:promptsRepo.folderSheet.namePh")}
								{...register("name", {
									required: t("workspaces:promptsRepo.folderSheet.nameRequired"),
									validate: (v) => v.trim().length > 0 || t("workspaces:promptsRepo.folderSheet.nameBlank"),
								})}
								autoFocus
							/>
							{errors.name && <p className="text-destructive text-xs">{errors.name.message}</p>}
						</div>

						<div className="space-y-2">
							<Label htmlFor="description">{t("workspaces:promptsRepo.folderSheet.descLabel")}</Label>
							<Textarea
								id="description"
								data-testid="folder-description-input"
								placeholder={t("workspaces:promptsRepo.folderSheet.descPh")}
								className="resize-none"
								{...register("description")}
							/>
						</div>
					</div>

					<SheetFooter className="mt-6 flex flex-row items-center justify-end gap-2 p-0">
						<Button type="button" variant="outline" data-testid="folder-cancel" onClick={() => onOpenChange(false)}>
							{t("common:actions.cancel")}
						</Button>
						<Button
							type="submit"
							data-testid="folder-submit"
							disabled={isLoading || !canSave}
							title={canSave ? undefined : t("workspaces:promptsRepo.folderSheet.noPermission")}
						>
							{isLoading ? t("workspaces:formFooter.saving") : isEditing ? t("common:actions.update") : t("common:actions.create")}
						</Button>
					</SheetFooter>
				</form>
			</SheetContent>
		</Sheet>
	);
}