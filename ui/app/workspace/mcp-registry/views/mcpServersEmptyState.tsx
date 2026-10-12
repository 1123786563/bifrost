import { Button } from "@/components/ui/button";
import { Link } from "@tanstack/react-router";
import { ArrowUpRight, Boxes, Server } from "lucide-react";
import { useTranslation } from "react-i18next";

const MCP_SERVERS_DOCS_URL = "https://docs.getbifrost.ai/features/mcp/overview";

interface MCPServersEmptyStateProps {
	onAddClick: () => void;
	canCreate?: boolean;
}

export function MCPServersEmptyState({ onAddClick, canCreate = true }: MCPServersEmptyStateProps) {
	const { t } = useTranslation(["common", "mcp"]);
	return (
		<div className="flex min-h-[80vh] w-full flex-col items-center justify-center gap-4 py-16 text-center">
			<div className="text-muted-foreground">
				<Server className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">{t("mcp:registry.empty.title")}</h1>
				<div className="text-muted-foreground mx-auto mt-2 w-full max-w-[600px] text-sm font-normal">{t("mcp:registry.empty.body")}</div>
				<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
					<Button
						variant="outline"
						aria-label={t("mcp:registry.empty.readMoreAria")}
						data-testid="mcp-registry-button-read-more"
						onClick={() => {
							window.open(`${MCP_SERVERS_DOCS_URL}?utm_source=bfd`, "_blank", "noopener,noreferrer");
						}}
					>
						{t("mcp:agents.empty.readMore")} <ArrowUpRight className="text-muted-foreground h-3 w-3" />
					</Button>
					<Button onClick={onAddClick} disabled={!canCreate} data-testid="create-mcp-client-btn">
						{t("mcp:registry.empty.addServer")}
					</Button>
					<Button asChild data-testid="mcp-library-empty-link-btn">
						<Link to="/workspace/mcp-registry/library">
							<Boxes className="h-4 w-4" />
							{t("mcp:registry.empty.browseLibrary")}
						</Link>
					</Button>
				</div>
			</div>
		</div>
	);
}