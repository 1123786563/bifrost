import FullPageLoader from "@/components/fullPageLoader";
import PageTitle from "@/components/pageTitle";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { getErrorMessage, useGetCoreConfigQuery, useUpdateCoreConfigMutation } from "@/lib/store";
import { CoreConfig, DefaultCoreConfig } from "@/lib/types/config";
import { SecretVar } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

const secretVarEquals = (a?: SecretVar, b?: SecretVar) =>
	(a?.value ?? "") === (b?.value ?? "") && (a?.ref ?? "") === (b?.ref ?? "") && (a?.type ?? "plain_text") === (b?.type ?? "plain_text");

export default function AgentGatewayView() {
	const { t } = useTranslation(["common", "settings"]);
	const hasSettingsUpdateAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	const { data: bifrostConfig, isLoading: isConfigLoading, error: configError, refetch } = useGetCoreConfigQuery({ fromDB: true });
	const config = bifrostConfig?.client_config;
	const [updateCoreConfig, { isLoading }] = useUpdateCoreConfigMutation();
	const [localConfig, setLocalConfig] = useState<CoreConfig>(DefaultCoreConfig);
	const draftRevision = useRef(0);
	const syncedRevision = useRef(0);

	useEffect(() => {
		if (bifrostConfig && config && draftRevision.current === syncedRevision.current) {
			setLocalConfig(config);
		}
	}, [config, bifrostConfig]);

	const hasChanges = useMemo(() => {
		if (!config) return false;
		return (
			!secretVarEquals(localConfig.a2a_external_client_url, config.a2a_external_client_url) ||
			Boolean(localConfig.a2a_store_raw_request_response) !== Boolean(config.a2a_store_raw_request_response)
		);
	}, [config, localConfig]);

	const handleExternalURLChange = useCallback(
		(value: SecretVar) => {
			draftRevision.current += 1;
			if (
				secretVarEquals(value, config?.a2a_external_client_url) &&
				Boolean(localConfig.a2a_store_raw_request_response) === Boolean(config?.a2a_store_raw_request_response)
			) {
				syncedRevision.current = draftRevision.current;
			}
			setLocalConfig((prev) => ({ ...prev, a2a_external_client_url: value }));
		},
		[config?.a2a_external_client_url, config?.a2a_store_raw_request_response, localConfig.a2a_store_raw_request_response],
	);

	const handleSave = useCallback(async () => {
		const savedRevision = draftRevision.current;
		try {
			if (!bifrostConfig) {
				toast.error(t("settings:shared.notLoaded"));
				return;
			}
			await updateCoreConfig({
				...bifrostConfig,
				client_config: {
					...bifrostConfig.client_config,
					a2a_external_client_url: localConfig.a2a_external_client_url,
					a2a_store_raw_request_response: Boolean(localConfig.a2a_store_raw_request_response),
				},
			}).unwrap();
			syncedRevision.current = savedRevision;
			toast.success(t("settings:agentGateway.saved"));
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	}, [t, bifrostConfig, localConfig, updateCoreConfig, t]);

	if (isConfigLoading && !bifrostConfig) {
		return <FullPageLoader />;
	}

	if (configError && !bifrostConfig) {
		return (
			<div className="mx-auto w-full max-w-7xl space-y-4 px-4 py-6" data-testid="agent-gateway-settings-load-error">
				<PageTitle title={t("settings:agentGateway.title")}>{t("settings:agentGateway.subtitle")}</PageTitle>
				<div className="border-destructive/30 bg-destructive/5 flex flex-col gap-3 rounded-sm border p-4">
					<p className="text-destructive text-sm font-medium">{t("settings:agentGateway.loadError")}</p>
					<p className="text-muted-foreground text-sm">{getErrorMessage(configError)}</p>
					<div>
						<Button variant="outline" size="sm" onClick={() => void refetch()}>
							{t("common:actions.retry")}
						</Button>
					</div>
				</div>
			</div>
		);
	}

	return (
		<div className="mx-auto w-full max-w-7xl space-y-4 px-4 py-6" data-testid="agent-gateway-settings-view">
			<PageTitle title={t("settings:agentGateway.title")}>{t("settings:agentGateway.subtitle")}</PageTitle>
			<div className="space-y-4">
				<div className="space-y-2 rounded-sm border p-4">
					<label htmlFor="a2a-external-client-url" className="text-sm font-medium">
						{t("settings:agentGateway.externalClientUrl")}
					</label>
					<p className="text-muted-foreground text-sm">
						{t("settings:agentGateway.externalUrlDescription1")} <code className="text-xs">Host</code>{" "}
						{t("settings:agentGateway.externalUrlDescription2")} <b>{t("settings:shared.pushNotificationsDisabled")}</b>
						{t("settings:agentGateway.externalUrlDescription3")} <code className="text-xs">env.BIFROST_EXTERNAL_URL</code>).
					</p>
					<SecretVarInput
						id="a2a-external-client-url"
						data-testid="a2a-external-client-url-input"
						placeholder={t("settings:agentGateway.externalUrlPlaceholder")}
						value={localConfig.a2a_external_client_url}
						onChange={handleExternalURLChange}
						disabled={!config || !hasSettingsUpdateAccess}
					/>
				</div>
			</div>
			<div className="flex items-center justify-between gap-4 rounded-sm border p-4">
				<div className="space-y-1">
					<label htmlFor="a2a-raw-capture" className="text-sm font-medium">
						Capture Raw Request and Response
					</label>
					<p className="text-muted-foreground text-sm">
						Persist raw request and response payloads in log records. Raw capture is disabled for gRPC because it uses binary protobuf
						messages; a size-limited capture can cut through a message and cannot be reliably decoded or inspected.
					</p>
				</div>
				<Switch
					id="a2a-raw-capture"
					data-testid="a2a-raw-capture-switch"
					checked={Boolean(localConfig.a2a_store_raw_request_response)}
					disabled={!config || !hasSettingsUpdateAccess}
					onCheckedChange={(checked) => {
						draftRevision.current += 1;
						if (
							checked === Boolean(config?.a2a_store_raw_request_response) &&
							secretVarEquals(localConfig.a2a_external_client_url, config?.a2a_external_client_url)
						) {
							syncedRevision.current = draftRevision.current;
						}
						setLocalConfig((prev) => ({ ...prev, a2a_store_raw_request_response: checked }));
					}}
				/>
			</div>
			<div className="flex justify-end pt-2">
				<Button
					onClick={handleSave}
					disabled={!hasChanges || isLoading || !hasSettingsUpdateAccess}
					data-testid="agent-gateway-settings-save-btn"
				>
					{isLoading ? t("common:state.saving") : t("settings:shared.saveChanges")}
				</Button>
			</div>
		</div>
	);
}