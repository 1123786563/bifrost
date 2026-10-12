import PageTitle from "@/components/pageTitle";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { MultiSelect } from "@/components/ui/multiSelect";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { getErrorMessage, useGetCoreConfigQuery, useUpdateCoreConfigMutation } from "@/lib/store";
import { RequestTypeLabels, RequestTypes } from "@/lib/constants/logs";
import { CoreConfig, DefaultCoreConfig } from "@/lib/types/config";
import { parseArrayFromText } from "@/lib/utils/array";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

const requestTypeLabel = (requestType: string): string =>
	Object.hasOwn(RequestTypeLabels, requestType) ? RequestTypeLabels[requestType as keyof typeof RequestTypeLabels] : requestType;

// Every known request type, plus any stored value the UI does not know about yet
// (for example a type set from config.json on a newer server) so it stays selectable.
const hiddenRequestTypeOptions = (selected: string[]) => {
	const known = new Set<string>(RequestTypes);
	const extra = selected.filter((requestType) => !known.has(requestType));
	return [...RequestTypes, ...extra].map((requestType) => ({ value: requestType, label: requestTypeLabel(requestType) }));
};

// Order-independent equality; the multi-select emits values in selection order.
const sameRequestTypes = (a: string[] | undefined, b: string[] | undefined): boolean => {
	const left = [...(a || [])].sort();
	const right = [...(b || [])].sort();
	return left.length === right.length && left.every((value, index) => value === right[index]);
};

export default function LoggingView() {
	const { t } = useTranslation(["common", "settings"]);
	const hasSettingsUpdateAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	const { data: bifrostConfig, isLoading: isConfigLoading, isError: isConfigError } = useGetCoreConfigQuery({ fromDB: true });
	const config = bifrostConfig?.client_config;
	const [updateCoreConfig, { isLoading }] = useUpdateCoreConfigMutation();
	const [localConfig, setLocalConfig] = useState<CoreConfig>(DefaultCoreConfig);
	const [needsRestart, setNeedsRestart] = useState<boolean>(false);
	const [loggingHeadersText, setLoggingHeadersText] = useState<string>("");

	useEffect(() => {
		if (config) {
			setLocalConfig(config);
			setLoggingHeadersText(config.logging_headers?.join(", ") || "");
		}
	}, [config]);

	const hasChanges = useMemo(() => {
		if (!config) return false;
		return (
			localConfig.enable_logging !== config.enable_logging ||
			localConfig.disable_content_logging !== config.disable_content_logging ||
			localConfig.retain_content_in_object_storage !== config.retain_content_in_object_storage ||
			localConfig.allow_per_request_content_storage_override !== config.allow_per_request_content_storage_override ||
			localConfig.allow_per_request_raw_override !== config.allow_per_request_raw_override ||
			localConfig.log_retention_days !== config.log_retention_days ||
			localConfig.hide_deleted_virtual_keys_in_filters !== config.hide_deleted_virtual_keys_in_filters ||
			JSON.stringify(localConfig.logging_headers || []) !== JSON.stringify(config.logging_headers || []) ||
			!sameRequestTypes(localConfig.hidden_request_types, config.hidden_request_types)
		);
	}, [config, localConfig]);

	const handleConfigChange = useCallback((field: keyof CoreConfig, value: boolean | number | string[]) => {
		setLocalConfig((prev) => ({ ...prev, [field]: value }));
		// Only enable_logging requires a restart (logging plugin is registered/skipped at startup).
		// disable_content_logging is read live via pointer by the logging plugin and applies on the next request.
		if (field === "enable_logging") {
			setNeedsRestart(true);
		}
	}, []);

	const handleLoggingHeadersChange = useCallback((value: string) => {
		setLoggingHeadersText(value);
		setLocalConfig((prev) => ({ ...prev, logging_headers: parseArrayFromText(value) }));
	}, []);

	const handleSave = useCallback(async () => {
		if (!bifrostConfig) {
			toast.error(t("settings:logging.notLoaded"));
			return;
		}

		// Validate log retention days
		if (localConfig.log_retention_days < 1) {
			toast.error(t("settings:logging.retentionMin"));
			return;
		}

		try {
			await updateCoreConfig({ ...bifrostConfig, client_config: localConfig }).unwrap();
			toast.success(t("settings:logging.saved"));
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	}, [t, bifrostConfig, localConfig, updateCoreConfig]);

	return (
		<div className="mx-auto w-full max-w-4xl space-y-4 px-4 py-6 md:px-0">
			<PageTitle title={t("settings:logging.title")}>{t("settings:logging.subtitle")}</PageTitle>

			<div className="space-y-4">
				{/* Enable Logs */}
				<div>
					<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
						<div className="space-y-0.5">
							<label htmlFor="enable-logging" className="text-sm font-medium">
								{t("settings:logging.enableLogs")}
							</label>
							<p className="text-muted-foreground text-sm">
								Enable logging of requests and responses to a SQL database. This can add 40-60mb of overhead to the system memory.
								{!bifrostConfig?.is_logs_connected && (
									<span className="text-destructive font-medium">{t("settings:logging.enableLogsRequires")}</span>
								)}
							</p>
						</div>
						<Switch
							id="enable-logging"
							size="md"
							checked={localConfig.enable_logging && bifrostConfig?.is_logs_connected}
							disabled={!bifrostConfig?.is_logs_connected}
							onCheckedChange={(checked) => {
								if (bifrostConfig?.is_logs_connected) {
									handleConfigChange("enable_logging", checked);
								}
							}}
						/>
					</div>
					{needsRestart && <RestartWarning />}
				</div>

				<div className="min-w-0 space-y-3 rounded-sm border p-4" data-testid="workspace-hidden-request-types">
					<div className="space-y-0.5">
						<Label htmlFor="hidden-request-types" className="text-sm font-medium">
							{t("settings:logging.hiddenRequestTypes")}
						</Label>
						<p className="text-muted-foreground text-sm">
							{t("settings:logging.hiddenDescriptionBefore")} <code className="text-xs">client.hidden_request_types</code>{" "}
							{t("settings:logging.hiddenDescriptionAfter")}
						</p>
					</div>
					{isConfigError ? (
						<p className="text-destructive text-sm" role="alert">
							{t("settings:logging.hiddenLoadError")}
						</p>
					) : (
						<MultiSelect
							id="hidden-request-types"
							data-testid="hidden-request-types-select"
							options={hiddenRequestTypeOptions(localConfig.hidden_request_types || [])}
							defaultValue={localConfig.hidden_request_types || []}
							resetOnDefaultValueChange
							onValueChange={(values) => handleConfigChange("hidden_request_types", values)}
							placeholder={isConfigLoading ? t("settings:logging.hiddenLoadingPlaceholder") : t("settings:logging.hiddenAllVisible")}
							emptyIndicator={t("settings:logging.hiddenEmpty")}
							disabled={!bifrostConfig || !hasSettingsUpdateAccess}
							maxCount={6}
							className="border-input text-foreground hover:bg-accent hover:text-accent-foreground min-h-9 rounded-sm bg-transparent font-normal"
						/>
					)}
				</div>

				{/* Disable Content Logging - Only show when logging is enabled */}
				{localConfig.enable_logging && bifrostConfig?.is_logs_connected && (
					<div>
						<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
							<div className="space-y-0.5">
								<label htmlFor="disable-content-logging" className="text-sm font-medium">
									{t("settings:logging.disableContentLogging")}
								</label>
								<p className="text-muted-foreground text-sm">
									{t("settings:logging.disableContentLoggingD1")} <code className="text-xs">store_raw_request_response</code>{" "}
									{t("settings:logging.disableContentLoggingD2")} <code className="text-xs">send_back_raw_*</code>{" "}
									{t("settings:logging.disableContentLoggingD3")}
								</p>
							</div>
							<Switch
								id="disable-content-logging"
								size="md"
								checked={localConfig.disable_content_logging}
								onCheckedChange={(checked) => handleConfigChange("disable_content_logging", checked)}
							/>
						</div>
					</div>
				)}

				{/* Retain Content in Object Storage - Only show when logging is enabled */}
				{localConfig.enable_logging && bifrostConfig?.is_logs_connected && (
					<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
						<div className="space-y-0.5">
							<label htmlFor="retain-content-in-object-storage" className="text-sm font-medium">
								{t("settings:logging.retainContent")}
							</label>
							<p className="text-muted-foreground text-sm">
								When enabled, requests with content logging disabled (via the global setting above or the{" "}
								<code className="text-xs">x-bf-disable-content-logging</code> header) still have their full content offloaded to object
								storage, but the content is never shown in logs: the database row stays metadata-only and the UI/API never fetch the payload
								back. Content is then only readable with direct access to the storage bucket. When disabled, content for such requests is
								dropped entirely (current behavior).
								{!bifrostConfig?.is_object_storage_connected && (
									<span className="text-destructive font-medium">
										{" "}
										Requires object storage to be configured on the logs store in config.json.
									</span>
								)}
							</p>
						</div>
						<Switch
							id="retain-content-in-object-storage"
							data-testid="workspace-retain-content-in-object-storage-switch"
							size="md"
							checked={localConfig.retain_content_in_object_storage && bifrostConfig?.is_object_storage_connected === true}
							disabled={!bifrostConfig?.is_object_storage_connected}
							onCheckedChange={(checked) => {
								if (bifrostConfig?.is_object_storage_connected) {
									handleConfigChange("retain_content_in_object_storage", checked);
								}
							}}
						/>
					</div>
				)}

				{/* Allow Per-Request Content Storage Override - Only show when logging is enabled */}
				{localConfig.enable_logging && bifrostConfig?.is_logs_connected && (
					<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
						<div className="space-y-0.5">
							<label htmlFor="allow-per-request-content-storage-override" className="text-sm font-medium">
								{t("settings:logging.allowPerRequestContent")}
							</label>
							<p className="text-muted-foreground text-sm">
								When enabled, individual requests can override the global content logging setting using the{" "}
								<code className="text-xs">x-bf-disable-content-logging</code> header or context key, and can opt-in to persisting raw
								provider bytes in logs using the <code className="text-xs">x-bf-store-raw-request-response</code> header. Raw-byte storage
								requires content logging to be on, either globally, or via{" "}
								<code className="text-xs">x-bf-disable-content-logging: false</code> on the same request. If content logging is off, raw
								bytes are dropped from the log record even when <code className="text-xs">x-bf-store-raw-request-response: true</code>. Does
								not control sending raw bytes back to callers; see Allow Per-Request Raw Override.
							</p>
						</div>
						<Switch
							id="allow-per-request-content-storage-override"
							data-testid="workspace-content-storage-override-switch"
							size="md"
							checked={localConfig.allow_per_request_content_storage_override}
							onCheckedChange={(checked) => handleConfigChange("allow_per_request_content_storage_override", checked)}
						/>
					</div>
				)}

				{/* Allow Per-Request Raw Override */}
				<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
					<div className="space-y-0.5">
						<label htmlFor="allow-per-request-raw-override" className="text-sm font-medium">
							{t("settings:logging.allowRawOverride")}
						</label>
						<p className="text-muted-foreground text-sm">
							When enabled, individual requests can send raw provider request/response bytes back to the caller using the{" "}
							<code className="text-xs">x-bf-send-back-raw-request</code> and <code className="text-xs">x-bf-send-back-raw-response</code>{" "}
							headers. Does not affect log storage; raw-byte persistence in logs is controlled by Allow Per-Request Content Storage
							Override.
						</p>
					</div>
					<Switch
						id="allow-per-request-raw-override"
						data-testid="workspace-raw-override-switch"
						size="md"
						checked={localConfig.allow_per_request_raw_override}
						onCheckedChange={(checked) => handleConfigChange("allow_per_request_raw_override", checked)}
					/>
				</div>

				{/* Log Retention Days */}
				{localConfig.enable_logging && bifrostConfig?.is_logs_connected && (
					<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
						<div className="space-y-0.5">
							<Label htmlFor="log-retention-days" className="text-sm font-medium">
								{t("settings:logging.retentionDays")}
							</Label>
							<p className="text-muted-foreground text-sm">{t("settings:logging.retentionDaysDescription")}</p>
						</div>
						<Input
							id="log-retention-days"
							type="number"
							min="1"
							value={localConfig.log_retention_days}
							onChange={(e) => {
								const value = parseInt(e.target.value) || 1;
								handleConfigChange("log_retention_days", Math.max(1, value));
							}}
							className="w-24"
						/>
					</div>
				)}

				<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
					<div className="space-y-0.5">
						<label htmlFor="hide-deleted-virtual-keys-in-filters" className="text-sm font-medium">
							{t("settings:logging.hideDeletedVK")}
						</label>
						<p className="text-muted-foreground text-sm">{t("settings:logging.hideDeletedVKDescription")}</p>
					</div>
					<Switch
						id="hide-deleted-virtual-keys-in-filters"
						data-testid="hide-deleted-virtual-keys-in-filters-switch"
						size="md"
						checked={localConfig.hide_deleted_virtual_keys_in_filters}
						onCheckedChange={(checked) => handleConfigChange("hide_deleted_virtual_keys_in_filters", checked)}
					/>
				</div>

				{/* Logging Headers */}
				{localConfig.enable_logging && bifrostConfig?.is_logs_connected && (
					<div className="space-y-2 rounded-sm border p-4">
						<label htmlFor="logging-headers" className="text-sm font-medium">
							{t("settings:logging.loggingHeaders")}
						</label>
						<p className="text-muted-foreground text-sm">
							{t("settings:logging.loggingHeadersD1")} <code className="text-xs">x-custom-*</code> {t("settings:logging.loggingHeadersD2")}{" "}
							<code className="text-xs">*</code> {t("settings:logging.loggingHeadersD3")} <code className="text-xs">*</code>{" "}
							{t("settings:logging.loggingHeadersD4")} <code className="text-xs">x-bf-lh-</code> {t("settings:logging.loggingHeadersD5")}
						</p>
						<Textarea
							id="logging-headers"
							data-testid="workspace-logging-headers-textarea"
							className="h-24"
							placeholder={t("settings:logging.headersPlaceholder")}
							value={loggingHeadersText}
							onChange={(e) => handleLoggingHeadersChange(e.target.value)}
						/>
					</div>
				)}
			</div>

			<div className="bg-card sticky bottom-0 flex justify-end py-2">
				<Button onClick={handleSave} disabled={!hasChanges || isLoading || !hasSettingsUpdateAccess}>
					{isLoading ? t("common:state.saving") : t("settings:shared.saveChanges")}
				</Button>
			</div>
		</div>
	);
}

const RestartWarning = () => {
	const { t } = useTranslation(["common", "settings"]);
	return <div className="text-muted-foreground mt-2 pl-4 text-xs font-semibold">{t("settings:shared.restartWarning")}</div>;
};