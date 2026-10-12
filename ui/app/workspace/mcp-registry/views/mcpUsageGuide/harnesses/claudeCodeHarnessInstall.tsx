import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useMemo, useState } from "react";
import { buildClaudeCodeCommand } from "../commandBuilders";
import { useTranslation } from "react-i18next";
import { HarnessCommandSection } from "../harnessCommandSection";
import type { ClaudeScope, HarnessInstallProps } from "../types";
import { getRegistrationLabel } from "../utils";

export function ClaudeCodeHarnessInstall({
	canGenerateCommand,
	clientConfig,
	emptyMessage,
	headers,
	selectedServers,
	serverScope,
}: HarnessInstallProps) {
	const { t } = useTranslation(["common", "mcp"]);
	const [scope, setScope] = useState<ClaudeScope>("local");

	const command = useMemo(
		() =>
			buildClaudeCodeCommand({
				clientConfig,
				headers,
				scope,
				selectedServers: serverScope === "selected" ? selectedServers : undefined,
			}),
		[clientConfig, headers, scope, selectedServers, serverScope],
	);

	return (
		<HarnessCommandSection
			canCopyCommand={canGenerateCommand}
			command={command}
			controls={
				<Select value={scope} onValueChange={(value) => setScope(value as ClaudeScope)}>
					<SelectTrigger className="w-32" data-testid="mcp-usage-guide-claude-scope" size="sm">
						<SelectValue />
					</SelectTrigger>
					<SelectContent>
						<SelectItem value="local">{t("mcp:usageGuide.scope.local")}</SelectItem>
						<SelectItem value="project">{t("mcp:usageGuide.scope.project")}</SelectItem>
						<SelectItem value="user">{t("mcp:usageGuide.scope.user")}</SelectItem>
					</SelectContent>
				</Select>
			}
			emptyMessage={emptyMessage}
			harnessName="Claude Code"
			logoSrc="/images/harness/claudecode.svg"
			registrationLabel={getRegistrationLabel(serverScope, selectedServers, t)}
		/>
	);
}