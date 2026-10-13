import { NoPermissionView } from "@/components/noPermissionView";
import { cn } from "@/lib/utils";
import { useTranslation } from "react-i18next";

interface Props {
	className?: string;
	icon: React.ReactNode;
	title: string;
	description: string;
	readmeLink: string;
	align?: "middle" | "top";
	testIdPrefix?: string;
}

/**
 * OSS fallback for the enterprise upsell screen. Every enterprise-only route
 * renders this behind its feature view; in OSS builds the feature does not
 * exist, so instead of the "Unlock … enterprise license" pitch the user sees
 * the standard no-permission state.
 */
export default function ContactUsView({ title, className, align = "middle" }: Props) {
	const { t } = useTranslation(["workspaces"]);
	const entity = title.replace(/^Unlock\s+/i, "").trim();
	return <NoPermissionView entity={entity} align={align} className={cn(className)} />;
}