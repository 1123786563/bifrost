import { MCPClientConfigEntry, MCPClientConfigsEditor } from "@/components/mcp/mcpClientConfigsEditor";
import { VirtualMCPToolSpec } from "@/lib/types/virtualMcps";
import { Trans, useTranslation } from "react-i18next";

const TOOL_WILDCARD = "*";

interface VirtualMcpToolsEditorProps {
	value: VirtualMCPToolSpec[];
	onChange: (specs: VirtualMCPToolSpec[]) => void;
}

// Adapts id-native Virtual MCP specs to the shared editor, which resolves names and tools.
// A deleted server keeps its id (name falls back to it), so edits don't drop it.
export default function VirtualMcpToolsEditor({ value, onChange }: VirtualMcpToolsEditorProps) {
	const { t } = useTranslation(["common", "mcp"]);
	const toolsTooltip = (
		<p>
			<Trans t={t} i18nKey="mcp:virtualMcps.tools.tooltip" components={{ em: <span className="font-medium" /> }}>
				Pick which of your MCP servers this Virtual MCP exposes and, for each, which tools. After adding a server, select specific tools or
				choose <em>{t("workspaces:virtualMcps.allowAllTools")}</em> to expose all of them.
			</Trans>
		</p>
	);
	const editorValue: MCPClientConfigEntry[] = value.map((spec) => ({
		mcp_client_id: spec.mcp_client_id,
		mcp_client_name: spec.mcp_client_id,
		tools_to_execute: spec.tool_names,
	}));

	const handleChange = (entries: MCPClientConfigEntry[]) => {
		onChange(
			entries.map((entry) => ({
				mcp_client_id: entry.mcp_client_id ?? entry.mcp_client_name,
				tool_names: entry.tools_to_execute ?? [TOOL_WILDCARD],
			})),
		);
	};

	return (
		<MCPClientConfigsEditor
			value={editorValue}
			onChange={handleChange}
			label={t("mcp:virtualMcps.tab.tools")}
			tooltip={toolsTooltip}
			allClientTools
			emptyState={
				<div className="text-muted-foreground rounded-md border border-dashed p-6 text-center text-sm">
					{t("mcp:virtualMcps.tools.empty")}
				</div>
			}
		/>
	);
}