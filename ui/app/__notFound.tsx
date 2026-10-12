import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";

export function NotFoundComponent() {
	const { t } = useTranslation(["common", "errors"]);

	return (
		<main className="h-base flex items-center justify-center p-6">
			<div className="mx-auto w-full max-w-md text-center">
				<p className="text-foreground text-7xl font-bold tracking-tight">404</p>
				<h1 className="text-foreground mt-4 text-2xl font-semibold">{t("errors:notFound.title")}</h1>
				<p className="text-muted-foreground mt-2 text-sm">{t("errors:notFound.description")}</p>
				<div className="mt-6 flex items-center justify-center gap-3">
					<Link
						data-testid="not-found-go-home-link"
						to="/workspace/logs"
						className="bg-primary text-primary-foreground focus-visible:ring-primary inline-flex items-center rounded-sm px-4 py-2 text-sm font-medium transition-opacity hover:opacity-90 focus-visible:ring-2 focus-visible:ring-offset-2 focus-visible:outline-none"
					>
						{t("errors:notFound.goHome")}
					</Link>
				</div>
			</div>
		</main>
	);
}