import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useMemo, useState } from "react";
import { buildVSCodeConfig, buildVSCodeDeeplink } from "../commandBuilders";
import { useTranslation } from "react-i18next";
import { HarnessCommandSection } from "../harnessCommandSection";
import type { HarnessInstallProps, VSCodeConfigScope } from "../types";
import { getRegistrationLabel } from "../utils";

export function VSCodeHarnessInstall({
	canGenerateCommand,
	clientConfig,
	emptyMessage,
	headers,
	platform,
	selectedServers,
	serverScope,
}: HarnessInstallProps) {
	const { t } = useTranslation(["common", "mcp"]);
	const [configScope, setConfigScope] = useState<VSCodeConfigScope>("workspace");

	const serverArgs = useMemo(
		() => ({
			clientConfig,
			headers,
			selectedServers: serverScope === "selected" ? selectedServers : undefined,
		}),
		[clientConfig, headers, selectedServers, serverScope],
	);

	const config = useMemo(() => buildVSCodeConfig(serverArgs), [serverArgs]);

	const deeplink = useMemo(() => buildVSCodeDeeplink(serverArgs), [serverArgs]);

	const userConfigPath = {
		linux: "~/.config/Code/User/mcp.json",
		macos: "~/Library/Application Support/Code/User/mcp.json",
		windows: "%APPDATA%/Code/User/mcp.json",
	}[platform];
	const configPath = configScope === "workspace" ? ".vscode/mcp.json" : userConfigPath;

	return (
		<HarnessCommandSection
			canCopyCommand={canGenerateCommand}
			command={config}
			controls={
				<Select value={configScope} onValueChange={(value) => setConfigScope(value as VSCodeConfigScope)}>
					<SelectTrigger className="w-32" data-testid="mcp-usage-guide-vscode-config-scope" size="sm">
						<SelectValue />
					</SelectTrigger>
					<SelectContent>
						<SelectItem value="workspace">{t("mcp:usageGuide.scope.workspace")}</SelectItem>
						<SelectItem value="user">{t("mcp:usageGuide.scope.user")}</SelectItem>
					</SelectContent>
				</Select>
			}
			copySuccessMessage={t("mcp:usageGuide.configCopied")}
			deeplink={deeplink}
			emptyMessage={emptyMessage}
			harnessName="VS Code"
			label={t("mcp:usageGuide.configLabel")}
			logoSrc="/images/harness/vscode.svg"
			registrationLabel={`${configPath} · ${getRegistrationLabel(serverScope, selectedServers, t)}`}
		/>
	);
}