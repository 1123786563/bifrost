import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { createFileRoute } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import MCPLibraryPage from "./page";

function RouteComponent() {
	const { t } = useTranslation(["common", "mcp"]);
	const hasMCPGatewayAccess = useRbac(RbacResource.MCPGateway, RbacOperation.View);
	if (!hasMCPGatewayAccess) {
		return <NoPermissionView entity={t("mcp:permissions.gatewayLibrary")} />;
	}
	return <MCPLibraryPage />;
}

export const Route = createFileRoute("/workspace/mcp-registry/library")({
	component: RouteComponent,
});