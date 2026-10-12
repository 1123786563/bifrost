import { createFileRoute } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import MCPSettingsPage from "./page";

function RouteComponent() {
	const { t } = useTranslation(["common", "mcp"]);
	const hasMCPGatewayAccess = useRbac(RbacResource.MCPGateway, RbacOperation.Update);
	const hasSettingsAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	if (!hasMCPGatewayAccess || !hasSettingsAccess) {
		return <NoPermissionView entity={t("mcp:permissions.gatewaySettings")} />;
	}
	return <MCPSettingsPage />;
}

export const Route = createFileRoute("/workspace/mcp-settings")({
	component: RouteComponent,
});