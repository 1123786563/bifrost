// Confirmation dialog for revoking an OAuth grant. Open/confirm are driven by
// the page; the copy explains that the refresh token stops rotating immediately
// while the current short-lived access token keeps working until it expires.

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
import { useTranslation } from "react-i18next";

interface RevokeGrantDialogProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	onConfirm: () => void;
}

export default function RevokeGrantDialog({ open, onOpenChange, onConfirm }: RevokeGrantDialogProps) {
	const { t } = useTranslation(["common", "auth"]);
	return (
		<AlertDialog open={open} onOpenChange={onOpenChange}>
			<AlertDialogContent>
				<AlertDialogHeader>
					<AlertDialogTitle>{t("auth:grants.revokeDialog.title")}</AlertDialogTitle>
					<AlertDialogDescription>
						{t("auth:grants.revokeDialog.descriptionBefore")} <code className="bg-muted rounded px-1 py-0.5 text-xs">/mcp</code>{" "}
						{t("auth:grants.revokeDialog.descriptionAfter")}
					</AlertDialogDescription>
				</AlertDialogHeader>
				<AlertDialogFooter>
					<AlertDialogCancel data-testid="oauth-grants-revoke-cancel-btn">{t("common:actions.cancel")}</AlertDialogCancel>
					<AlertDialogAction data-testid="oauth-grants-revoke-confirm-btn" onClick={onConfirm}>
						{t("auth:grants.revokeDialog.confirm")}
					</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}