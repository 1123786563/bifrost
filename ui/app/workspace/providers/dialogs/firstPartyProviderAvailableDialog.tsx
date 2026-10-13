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
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderLabels } from "@/lib/constants/logs";
import { KnownProvider } from "@/lib/types/config";
import { useTranslation } from "react-i18next";

interface Props {
	show: boolean;
	customProviderName: string;
	knownProvider: KnownProvider;
	onDismiss: () => void;
	onProceed: () => void;
}

export default function FirstPartyProviderAvailableDialog({ show, customProviderName, knownProvider, onDismiss, onProceed }: Props) {
	const { t } = useTranslation(["common", "providers"]);
	const label = ProviderLabels[knownProvider];
	// The exact name now belongs to the first-party provider, which the backend enforces: a custom
	// provider holding it can no longer be updated, and config.json skips it.
	const nameReserved = customProviderName.trim().toLowerCase() === knownProvider;

	return (
		<AlertDialog open={show}>
			<AlertDialogContent data-testid="first-party-provider-available-dialog">
				<AlertDialogHeader>
					<AlertDialogTitle className="flex items-center gap-2">
						<RenderProviderIcon provider={knownProvider as ProviderIconType} size="sm" className="h-5 w-5 shrink-0" />
						{t("providers:firstParty.title", { label })}
					</AlertDialogTitle>
					{nameReserved ? (
						<AlertDialogDescription data-testid="first-party-provider-name-reserved">
							{t("providers:firstParty.nameReserved1", { label })} <span className="text-foreground font-medium">{customProviderName}</span>{" "}
							{t("providers:firstParty.nameReserved2")} <span className="text-foreground font-medium">{knownProvider}-custom</span>
							{t("providers:firstParty.nameReserved3")}
						</AlertDialogDescription>
					) : (
						<AlertDialogDescription>
							{t("providers:firstParty.description1")} <span className="text-foreground font-medium">{customProviderName}</span>
							{t("providers:firstParty.description2", { label })}{" "}
							<span className="text-foreground font-medium">{t("providers:addProvider.triggerAria")}</span>
							{t("providers:firstParty.description3", { label })}
						</AlertDialogDescription>
					)}
				</AlertDialogHeader>
				<AlertDialogFooter>
					<AlertDialogCancel onClick={onDismiss}>{t("providers:firstParty.keepCustom")}</AlertDialogCancel>
					<AlertDialogAction onClick={onProceed}>{t("providers:firstParty.takeMeThere")}</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}