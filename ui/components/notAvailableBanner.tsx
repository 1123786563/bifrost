import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Database } from "lucide-react";
import { useTranslation } from "react-i18next";

const NotAvailableBanner = () => {
	const { t } = useTranslation(["common", "workspaces"]);
	return (
		<div className="h-base flex items-center justify-center p-4">
			<div className="w-full max-w-md">
				<Alert className="border-destructive/50 text-destructive/50 dark:text-destructive/70 dark:border-destructive/70 [&>svg]:text-destructive dark:bg-card bg-red-50">
					<AlertTitle className="flex items-center gap-2">
						<Database className="dark:text-destructive/70 text-destructive/50 h-4 w-4" />
						{t("workspaces:notAvailable.title")}
					</AlertTitle>
					<AlertDescription className="mt-2 space-y-2 text-xs">
						<div>{t("workspaces:notAvailable.description")}</div>
						<div className="text-muted-foreground">
							{t("workspaces:notAvailable.howToPrefix")}{" "}
							<a
								href="https://www.getmaxim.ai/bifrost/docs/quickstart/gateway/setting-up#two-configuration-modes"
								target="_blank"
								rel="noopener noreferrer"
								className="font-medium underline underline-offset-2"
								data-testid="config-store-documentation-link"
							>
								{t("workspaces:notAvailable.docsLink")}
							</a>
							{t("workspaces:notAvailable.howToSuffix")}
						</div>
					</AlertDescription>
				</Alert>
			</div>
		</div>
	);
};

export default NotAvailableBanner;