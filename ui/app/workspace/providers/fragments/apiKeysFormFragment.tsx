import { ModelAccessSelector } from "@/components/modelAccess";
import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from "@/components/ui/accordion";
import { FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { DefaultVertexAWSWorkloadIdentityConfig, OAuthSigningAlgorithms } from "@/lib/types/config";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { TagInput } from "@/components/ui/tagInput";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { hasCopilotApiToken, isRedacted } from "@/lib/utils/validation";
import { Info } from "lucide-react";
import { useEffect, useState } from "react";
import { Control, UseFormReturn } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { DeploymentsTable } from "./deploymentsTable";

// Providers that support batch APIs
// Vertex authentication methods offered by the key form. The backend picks the mode from which
// fields are set; this discriminator only drives the tabs and validation.
type VertexAuthType = "service_account" | "service_account_json" | "api_key" | "aws_workload_identity";

const BATCH_SUPPORTED_PROVIDERS = ["openai", "bedrock", "anthropic", "gemini", "azure", "vertex", "wafer"];

interface Props {
	control: Control<any>;
	providerName: string;
	// For custom providers, the underlying base provider type (e.g. "bedrock").
	// Drives which credential UI renders; falls back to providerName for native providers.
	baseProviderType?: string;
	form: UseFormReturn<any>;
}

// Batch API form field for all providers
function BatchAPIFormField({ control }: { control: Control<any>; form: UseFormReturn<any> }) {
	const { t } = useTranslation(["common", "providers"]);
	return (
		<FormField
			control={control}
			name={`key.use_for_batch_api`}
			render={({ field }) => (
				<FormItem className="flex flex-row items-center justify-between rounded-sm border p-2">
					<div className="space-y-1.5">
						<FormLabel>{t("providers:keysForm.useForBatch")}</FormLabel>
						<FormDescription>{t("providers:keysForm.useForBatchDesc")}</FormDescription>
					</div>
					<FormControl>
						<Switch checked={field.value ?? false} onCheckedChange={field.onChange} />
					</FormControl>
				</FormItem>
			)}
		/>
	);
}

// AWS endpoint services Bifrost dials for Bedrock. `name` is the config field, `placeholder` the
// DNS name shape for that service - S3 differs from the rest, so each is spelled out.
const BEDROCK_VPC_ENDPOINT_SERVICES = [
	{
		name: "runtime",
		labelKey: "providers:keysForm.vpc.services.runtime",
		descriptionKey: "providers:keysForm.vpc.services.runtimeDesc",
		placeholder: "vpce-0abc123-x1y2z3.bedrock-runtime.us-east-1.vpce.amazonaws.com",
	},
	{
		name: "control_plane",
		labelKey: "providers:keysForm.vpc.services.controlPlane",
		descriptionKey: "providers:keysForm.vpc.services.controlPlaneDesc",
		placeholder: "vpce-0abc123-x1y2z3.bedrock.us-east-1.vpce.amazonaws.com",
	},
	{
		name: "mantle",
		labelKey: "providers:keysForm.vpc.services.mantle",
		descriptionKey: "providers:keysForm.vpc.services.mantleDesc",
		placeholder: "vpce-0abc123-x1y2z3.bedrock-mantle.us-east-1.vpce.amazonaws.com",
	},
	{
		name: "agent_runtime",
		labelKey: "providers:keysForm.vpc.services.agentRuntime",
		descriptionKey: "providers:keysForm.vpc.services.agentRuntimeDesc",
		placeholder: "vpce-0abc123-x1y2z3.bedrock-agent-runtime.us-east-1.vpce.amazonaws.com",
	},
	{
		name: "s3",
		labelKey: "providers:keysForm.vpc.services.s3",
		descriptionKey: "providers:keysForm.vpc.services.s3Desc",
		placeholder: "bucket.vpce-0abc123-x1y2z3.s3.us-east-1.vpce.amazonaws.com",
	},
];

// VPC endpoint host overrides for AWS PrivateLink. Collapsed by default: most deployments reach
// Bedrock over the public regional endpoints and never set these.
function VPCEndpointsFormField({
	control,
	configKey,
	services,
}: {
	control: Control<any>;
	configKey: string;
	services: typeof BEDROCK_VPC_ENDPOINT_SERVICES;
}) {
	const { t } = useTranslation(["common", "providers"]);
	return (
		<Accordion type="single" collapsible className="w-full">
			<AccordionItem value="vpc-endpoints" className="rounded-sm border px-2 last:border-b">
				<AccordionTrigger className="py-2 hover:no-underline" data-testid="bedrock-vpc-endpoints-trigger">
					<span className="block space-y-1.5 pr-2">
						<span className="block text-sm leading-none font-medium">{t("providers:keysForm.vpc.title")}</span>
						<span className="text-muted-foreground block text-sm font-normal">{t("providers:keysForm.vpc.description")}</span>
					</span>
				</AccordionTrigger>
				<AccordionContent className="space-y-4 pt-2 pb-3">
					{services.map((service) => (
						<FormField
							key={service.name}
							control={control}
							name={`${configKey}.endpoints.${service.name}`}
							render={({ field }) => (
								<FormItem>
									<FormLabel>{t(service.labelKey)}</FormLabel>
									<FormDescription>{t(service.descriptionKey)}</FormDescription>
									<FormControl>
										<SecretVarInput
											data-testid={`apikey-bedrock-endpoint-${service.name}-input`}
											placeholder={service.placeholder}
											{...field}
										/>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
					))}
				</AccordionContent>
			</AccordionItem>
		</Accordion>
	);
}

export function ApiKeyFormFragment({ control, providerName, baseProviderType, form }: Props) {
	const { t } = useTranslation(["common", "providers"]);
	// Credential UI keys off the base provider type for custom providers; the
	// model list, deployments table, and API calls still use the real providerName.
	const effectiveProvider = baseProviderType ?? providerName;
	const isBedrock = effectiveProvider === "bedrock";
	const isBedrockMantle = effectiveProvider === "bedrock_mantle";
	const isVertex = effectiveProvider === "vertex";
	const isAzure = effectiveProvider === "azure";
	const isReplicate = effectiveProvider === "replicate";
	const isVLLM = effectiveProvider === "vllm";
	const isOllama = effectiveProvider === "ollama";
	const isSGL = effectiveProvider === "sgl";
	const isDeepseek = effectiveProvider === "deepseek";
	const isFireworks = effectiveProvider === "fireworks";
	const isDatabricks = effectiveProvider === "databricks";
	const isGithubCopilot = effectiveProvider === "github-copilot";
	const isCloudflare = effectiveProvider === "cloudflare";
	// openai, and every custom provider built on the openai base, may mint its bearer from
	// oauth_key_config instead of carrying a static API key.
	const isOpenAICompatible = effectiveProvider === "openai";
	// Reactive, so the App-credential labels stay truthful. Once a Copilot token is present
	// those fields genuinely are optional, and a static "(Required)" would contradict the
	// section note telling the operator they can leave them blank.
	const copilotAppSuffix = hasCopilotApiToken(form.watch("key.value"))
		? t("providers:suffixes.optional")
		: t("providers:suffixes.required");
	const isKeylessProvider = isOllama || isSGL;
	const supportsBatchAPI = BATCH_SUPPORTED_PROVIDERS.includes(effectiveProvider);

	// Auth type state for Azure: 'api_key', 'entra_id', or 'default_credential'
	const [azureAuthType, setAzureAuthType] = useState<"api_key" | "entra_id" | "default_credential">("api_key");

	// Auth type state for Bedrock: 'iam_role', 'explicit', or 'api_key'
	const [bedrockAuthType, setBedrockAuthType] = useState<"iam_role" | "explicit" | "api_key">("iam_role");

	// Auth type state for Bedrock Mantle: 'iam_role', 'explicit', or 'api_key'
	const [bedrockMantleAuthType, setBedrockMantleAuthType] = useState<"iam_role" | "explicit" | "api_key">("iam_role");

	// Auth type state for Databricks: 'pat' (personal access token) or 'oauth_m2m' (service principal)
	const [databricksAuthType, setDatabricksAuthType] = useState<"pat" | "oauth_m2m">("pat");

	// Auth type state for openai-based keys: a static API key, or an OAuth-minted bearer
	const [openaiAuthType, setOpenaiAuthType] = useState<"api_key" | "oauth">("api_key");
	const oauthGrantType = form.watch("key.oauth_key_config.grant_type") ?? "client_credentials";

	// Auth type state for Vertex: 'service_account' (ADC), 'service_account_json', 'api_key', or
	// 'aws_workload_identity' (GCP Workload Identity Federation from the workload's AWS identity)
	const [vertexAuthType, setVertexAuthType] = useState<VertexAuthType>("service_account");

	// Detect auth type from existing form values when editing
	useEffect(() => {
		if (form.formState.isDirty) return;
		if (isAzure) {
			const clientId = form.getValues("key.azure_key_config.client_id");
			const clientSecret = form.getValues("key.azure_key_config.client_secret");
			const tenantId = form.getValues("key.azure_key_config.tenant_id");
			const apiKey = form.getValues("key.value");
			const hasEntraField =
				clientId?.value || clientId?.ref || clientSecret?.value || clientSecret?.ref || tenantId?.value || tenantId?.ref;
			const hasApiKey = apiKey?.value || apiKey?.ref;
			let detected: "api_key" | "entra_id" | "default_credential" = "api_key";
			if (hasEntraField) {
				detected = "entra_id";
			} else if (!hasApiKey) {
				detected = "default_credential";
			}
			setAzureAuthType(detected);
			form.setValue("key.azure_key_config._auth_type", detected);
		}
	}, [isAzure, form]);

	useEffect(() => {
		if (form.formState.isDirty) return;
		if (isVertex) {
			const wifAudience = form.getValues("key.vertex_key_config.aws_workload_identity.audience");
			const authCredentials = form.getValues("key.vertex_key_config.auth_credentials")?.value;
			const authCredentialsEnv = form.getValues("key.vertex_key_config.auth_credentials")?.ref;
			const apiKey = form.getValues("key.value")?.value;
			const apiKeyEnv = form.getValues("key.value")?.ref;
			let detected: VertexAuthType = "service_account";
			if (wifAudience?.value || wifAudience?.ref) {
				detected = "aws_workload_identity";
			} else if (authCredentials || authCredentialsEnv) {
				detected = "service_account_json";
			} else if (apiKey || apiKeyEnv) {
				detected = "api_key";
			}
			setVertexAuthType(detected);
			form.setValue("key.vertex_key_config._auth_type", detected);
		}
	}, [isVertex, form]);

	const databricksDefaults = form.formState.defaultValues?.key?.databricks_key_config;
	useEffect(() => {
		if (form.formState.isDirty) return;
		if (isDatabricks) {
			const clientId = form.getValues("key.databricks_key_config.client_id");
			const clientSecret = form.getValues("key.databricks_key_config.client_secret");
			const hasServicePrincipal = clientId?.value || clientId?.ref || clientSecret?.value || clientSecret?.ref;
			const detected: "pat" | "oauth_m2m" = hasServicePrincipal ? "oauth_m2m" : "pat";
			setDatabricksAuthType(detected);
			form.setValue("key.databricks_key_config._auth_type", detected);
		}
		// databricksDefaults re-runs detection after the key form resets itself, which
		// happens once the key resolves - after this effect has already run once against
		// an empty form and settled on the personal access token tab.
	}, [isDatabricks, form, databricksDefaults]);

	const oauthDefaults = form.formState.defaultValues?.key?.oauth_key_config;
	useEffect(() => {
		if (form.formState.isDirty) return;
		if (isOpenAICompatible) {
			const grant = form.getValues("key.oauth_key_config.grant_type");
			const tokenURL = form.getValues("key.oauth_key_config.token_url");
			const detected: "api_key" | "oauth" = grant || tokenURL?.value || tokenURL?.ref ? "oauth" : "api_key";
			setOpenaiAuthType(detected);
			form.setValue("key.oauth_key_config._auth_type", detected);
		}
		// oauthDefaults re-runs detection after the key form resets itself once the key
		// resolves, for the same reason as databricksDefaults above.
	}, [isOpenAICompatible, form, oauthDefaults]);

	useEffect(() => {
		if (form.formState.isDirty) return;
		if (isBedrock) {
			const accessKey = form.getValues("key.bedrock_key_config.access_key");
			const secretKey = form.getValues("key.bedrock_key_config.secret_key");
			const apiKey = form.getValues("key.value");
			const hasExplicitCreds = accessKey?.value || accessKey?.ref || secretKey?.value || secretKey?.ref;
			const hasApiKey = apiKey?.value || apiKey?.ref;
			let detected: "iam_role" | "explicit" | "api_key" = "iam_role";
			if (hasExplicitCreds) {
				detected = "explicit";
			} else if (hasApiKey) {
				detected = "api_key";
			}
			setBedrockAuthType(detected);
			form.setValue("key.bedrock_key_config._auth_type", detected);
		}
	}, [isBedrock, form]);

	useEffect(() => {
		if (form.formState.isDirty) return;
		if (isBedrockMantle) {
			const accessKey = form.getValues("key.bedrock_mantle_key_config.access_key");
			const secretKey = form.getValues("key.bedrock_mantle_key_config.secret_key");
			const apiKey = form.getValues("key.value");
			const hasExplicitCreds = accessKey?.value || accessKey?.ref || secretKey?.value || secretKey?.ref;
			const hasApiKey = apiKey?.value || apiKey?.ref;
			let detected: "iam_role" | "explicit" | "api_key" = "iam_role";
			if (hasExplicitCreds) {
				detected = "explicit";
			} else if (hasApiKey) {
				detected = "api_key";
			}
			setBedrockMantleAuthType(detected);
			form.setValue("key.bedrock_mantle_key_config._auth_type", detected);
		}
		// form.formState.defaultValues is a dependency so detection re-runs when ProviderKeyForm
		// repopulates an existing key via form.reset(...) after mount, not only on first render.
	}, [isBedrockMantle, form, form.formState.defaultValues]);

	return (
		<div data-tab="api-keys" className="space-y-4 overflow-hidden">
			<div className="flex items-start gap-4 px-0.5">
				<div className="flex-1">
					<FormField
						control={control}
						name={`key.name`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("common:fields.name")}</FormLabel>
								<FormControl>
									<Input placeholder={t("providers:keysForm.namePlaceholder")} type="text" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
				</div>
				<FormField
					control={control}
					name={`key.weight`}
					render={({ field }) => (
						<FormItem>
							<div className="flex items-center gap-2">
								<FormLabel>{t("providers:keysForm.weight")}</FormLabel>
								<TooltipProvider>
									<Tooltip>
										<TooltipTrigger asChild>
											<span>
												<Info className="text-muted-foreground h-3 w-3" />
											</span>
										</TooltipTrigger>
										<TooltipContent className="max-w-sm">
											<p>{t("providers:keysForm.weightTooltip")}</p>
										</TooltipContent>
									</Tooltip>
								</TooltipProvider>
							</div>
							<FormControl>
								<Input
									placeholder="1.0"
									className="w-[260px]"
									value={field.value === undefined || field.value === null ? "" : String(field.value)}
									onChange={(e) => {
										// Keep as string during typing to allow partial input
										field.onChange(e.target.value === "" ? "" : e.target.value);
									}}
									onBlur={(e) => {
										const v = e.target.value.trim();
										if (v !== "") {
											const num = parseFloat(v);
											if (!isNaN(num)) {
												field.onChange(num);
											}
										}
										field.onBlur();
									}}
									name={field.name}
									ref={field.ref}
									type="text"
								/>
							</FormControl>
							<FormMessage />
						</FormItem>
					)}
				/>
			</div>
			{/* Hide API Key field for providers with dedicated auth tabs */}
			{!isAzure && !isBedrock && !isBedrockMantle && !isVertex && !isDatabricks && !isOpenAICompatible && (
				<FormField
					control={control}
					name={`key.value`}
					render={({ field }) => (
						<FormItem className="px-0.5">
							<FormLabel>
								{isGithubCopilot ? t("providers:keysForm.copilotApiToken") : t("providers:keysForm.apiKey")}{" "}
								{isVLLM || isGithubCopilot ? t("providers:suffixes.optional") : ""}
							</FormLabel>
							{isGithubCopilot && <FormDescription>{t("providers:keysForm.copilotApiTokenDesc")}</FormDescription>}
							<FormControl>
								<SecretVarInput
									placeholder={
										isGithubCopilot ? t("providers:keysForm.copilotTokenPlaceholder") : t("providers:keysForm.apiKeyPlaceholder")
									}
									type="text"
									{...field}
								/>
							</FormControl>
							<FormMessage />
						</FormItem>
					)}
				/>
			)}
			<>
				<FormField
					control={control}
					name={`key.models`}
					render={({ field }) => (
						<FormItem className="px-0.5">
							<FormControl>
								<ModelAccessSelector
									mode="allow"
									data-testid="api-keys-models-multiselect"
									provider={providerName}
									unfiltered
									value={field.value || []}
									onChange={field.onChange}
									label={
										<>
											<FormLabel>{t("providers:keysForm.allowedModels")}</FormLabel>
											<TooltipProvider>
												<Tooltip>
													<TooltipTrigger asChild>
														<span>
															<Info className="text-muted-foreground h-3 w-3" />
														</span>
													</TooltipTrigger>
													<TooltipContent className="max-w-sm">
														<p>{t("providers:keysForm.allowedModelsTooltip")}</p>
													</TooltipContent>
												</Tooltip>
											</TooltipProvider>
										</>
									}
								/>
							</FormControl>
							<FormMessage />
						</FormItem>
					)}
				/>
				<FormField
					control={control}
					name={`key.blacklisted_models`}
					render={({ field }) => (
						<FormItem data-testid="apikey-blacklisted-models-field" className="px-0.5">
							<FormControl>
								<ModelAccessSelector
									mode="block"
									data-testid="api-keys-blocked-models-multiselect"
									provider={providerName}
									unfiltered
									value={field.value || []}
									onChange={field.onChange}
									label={
										<>
											<FormLabel>{t("providers:keysForm.blockedModels")}</FormLabel>
											<TooltipProvider>
												<Tooltip>
													<TooltipTrigger asChild>
														<span>
															<Info className="text-muted-foreground h-3 w-3" />
														</span>
													</TooltipTrigger>
													<TooltipContent className="max-w-sm">
														<p>{t("providers:keysForm.blockedModelsTooltip")}</p>
													</TooltipContent>
												</Tooltip>
											</TooltipProvider>
										</>
									}
								/>
							</FormControl>
							<FormMessage />
						</FormItem>
					)}
				/>
				<FormField
					control={control}
					name={`key.aliases`}
					render={({ field }) => (
						<FormItem data-testid="apikey-deployments-field" className="px-0.5">
							<FormLabel>{t("providers:keysForm.deploymentsLabel")}</FormLabel>
							<FormDescription>
								{t("providers:keysForm.deploymentsDesc")}
								{isReplicate && <> {t("providers:keysForm.deploymentsReplicateNote")}</>}
							</FormDescription>
							<FormControl>
								<div data-testid="apikey-deployments-table">
									<DeploymentsTable
										providerName={providerName}
										value={field.value}
										onChange={(next) => {
											form.clearErrors("key.aliases");
											field.onChange(Object.keys(next).length > 0 ? next : {});
										}}
									/>
								</div>
							</FormControl>
							<FormMessage />
						</FormItem>
					)}
				/>
			</>
			{supportsBatchAPI && !isBedrock && !isAzure && !isVertex && <BatchAPIFormField control={control} form={form} />}
			{isAzure && (
				<div className="space-y-4 px-0.5">
					<Separator className="my-6" />
					<div className="space-y-2">
						<FormLabel>{t("providers:keysForm.authMethod")}</FormLabel>
						<Tabs
							value={azureAuthType}
							onValueChange={(v) => {
								setAzureAuthType(v as "api_key" | "entra_id" | "default_credential");
								form.setValue("key.azure_key_config._auth_type", v, { shouldDirty: true, shouldValidate: true });
								if (v === "entra_id" || v === "default_credential") {
									// Clear API key when switching away from API Key
									form.setValue("key.value", undefined, { shouldDirty: true });
								}
								if (v === "api_key" || v === "default_credential") {
									// Clear Entra ID fields when switching away from Entra ID
									form.setValue("key.azure_key_config.client_id", undefined, { shouldDirty: true });
									form.setValue("key.azure_key_config.client_secret", undefined, { shouldDirty: true });
									form.setValue("key.azure_key_config.tenant_id", undefined, { shouldDirty: true });
									form.setValue("key.azure_key_config.scopes", undefined, { shouldDirty: true });
								}
							}}
						>
							<TabsList className="flex w-full justify-start">
								<TabsTrigger data-testid="apikey-azure-default-credential-tab" value="default_credential">
									{t("providers:keysForm.azure.defaultCredential")}
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-azure-api-key-tab" value="api_key">
									{t("providers:keysForm.apiKey")}
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-azure-entra-id-tab" value="entra_id">
									{t("providers:keysForm.azure.entraId")}
								</TabsTrigger>
							</TabsList>
						</Tabs>
					</div>
					{azureAuthType === "api_key" && (
						<FormField
							control={control}
							name={`key.value`}
							render={({ field }) => (
								<FormItem>
									<FormLabel>
										{t("providers:keysForm.apiKey")}{" "}
										{isVertex ? t("providers:keysForm.azureGeminiNote") : isVLLM ? t("providers:suffixes.optional") : ""}
									</FormLabel>
									<FormControl>
										<SecretVarInput placeholder={t("providers:keysForm.apiKeyPlaceholder")} type="text" {...field} />
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
					)}
					{azureAuthType === "default_credential" && (
						<p className="text-muted-foreground text-sm">{t("providers:keysForm.azure.defaultCredentialDesc")}</p>
					)}

					<FormField
						control={control}
						name={`key.azure_key_config.endpoint`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers:keysForm.azure.endpoint")}</FormLabel>
								<FormControl>
									<SecretVarInput placeholder="https://your-resource.openai.azure.com or env.AZURE_ENDPOINT" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					{azureAuthType === "entra_id" && (
						<>
							<FormField
								control={control}
								name={`key.azure_key_config.client_id`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers:keysForm.azure.clientId")}</FormLabel>
										<FormControl>
											<SecretVarInput placeholder="your-client-id or env.AZURE_CLIENT_ID" {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.azure_key_config.client_secret`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers:keysForm.azure.clientSecret")}</FormLabel>
										<FormControl>
											<SecretVarInput placeholder="your-client-secret or env.AZURE_CLIENT_SECRET" {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.azure_key_config.tenant_id`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers:keysForm.azure.tenantId")}</FormLabel>
										<FormControl>
											<SecretVarInput placeholder="your-tenant-id or env.AZURE_TENANT_ID" {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.azure_key_config.scopes`}
								render={({ field }) => (
									<FormItem>
										<div className="flex items-center gap-2">
											<FormLabel>{t("providers:keysForm.azure.scopes")}</FormLabel>
											<TooltipProvider>
												<Tooltip>
													<TooltipTrigger asChild>
														<span>
															<Info className="text-muted-foreground h-3 w-3" />
														</span>
													</TooltipTrigger>
													<TooltipContent>
														<p>{t("providers:keysForm.azure.scopesTooltip")}</p>
													</TooltipContent>
												</Tooltip>
											</TooltipProvider>
										</div>
										<FormControl>
											<TagInput
												data-testid="apikey-azure-scopes-input"
												placeholder={t("providers:keysForm.azure.scopesPlaceholder")}
												value={field.value ?? []}
												onValueChange={field.onChange}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</>
					)}
					{supportsBatchAPI && <BatchAPIFormField control={control} form={form} />}
				</div>
			)}
			{isVertex && (
				<div className="space-y-4">
					<Separator className="my-6" />
					<div className="space-y-2">
						<FormLabel>{t("providers:keysForm.authMethod")}</FormLabel>
						<Tabs
							value={vertexAuthType}
							onValueChange={(v) => {
								const next = v as VertexAuthType;
								setVertexAuthType(next);
								form.setValue("key.vertex_key_config._auth_type", next, { shouldDirty: true, shouldValidate: true });
								if (next !== "service_account_json") {
									// Clear auth credentials when switching away from service account JSON
									form.setValue("key.vertex_key_config.auth_credentials", undefined, { shouldDirty: true });
								}
								if (next !== "api_key") {
									// Clear the API key when switching away from API Key. The update endpoint has patch
									// semantics (an omitted field keeps its stored value), and the backend prefers a stored
									// API key over federation or ADC, so this must be an explicit empty value, not undefined.
									form.setValue("key.value", { value: "", ref: "" }, { shouldDirty: true });
								}
								if (next === "aws_workload_identity") {
									// Seed the block so its inputs are controlled from the first keystroke
									if (!form.getValues("key.vertex_key_config.aws_workload_identity")) {
										form.setValue(
											"key.vertex_key_config.aws_workload_identity",
											{ ...DefaultVertexAWSWorkloadIdentityConfig },
											{ shouldDirty: true },
										);
									}
								} else {
									// Drop the block entirely: an audience left behind would take precedence on the server
									form.setValue("key.vertex_key_config.aws_workload_identity", undefined, { shouldDirty: true });
								}
							}}
						>
							<TabsList className="flex w-full justify-start">
								<TabsTrigger data-testid="apikey-vertex-service-account-tab" value="service_account">
									{t("providers:keysForm.vertex.serviceAccountAttached")}
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-vertex-service-account-json-tab" value="service_account_json">
									{t("providers:keysForm.vertex.serviceAccountJson")}
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-vertex-aws-workload-identity-tab" value="aws_workload_identity">
									{t("providers:keysForm.vertex.workloadIdentityAws")}
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-vertex-api-key-tab" value="api_key">
									{t("providers:keysForm.apiKey")}
								</TabsTrigger>
							</TabsList>
						</Tabs>
						{vertexAuthType === "service_account" && (
							<p className="text-muted-foreground text-sm">{t("providers:keysForm.vertex.serviceAccountDesc")}</p>
						)}
						{vertexAuthType === "aws_workload_identity" && (
							<p className="text-muted-foreground text-sm">{t("providers:keysForm.vertex.workloadIdentityDesc")}</p>
						)}
					</div>

					<FormField
						control={control}
						name={`key.vertex_key_config.project_id`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers:keysForm.vertex.projectId")}</FormLabel>
								<FormControl>
									<SecretVarInput placeholder="your-gcp-project-id or env.VERTEX_PROJECT_ID" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name={`key.vertex_key_config.project_number`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers:keysForm.vertex.projectNumber")}</FormLabel>
								<FormControl>
									<SecretVarInput placeholder="your-gcp-project-number or env.VERTEX_PROJECT_NUMBER" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name={`key.vertex_key_config.region`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers:keysForm.vertex.region")}</FormLabel>
								<FormDescription>
									{t("providers:keysForm.vertex.regionDesc1")}{" "}
									<span className="font-medium">{t("providers:keysForm.vertex.forceSingleRegion")}</span>
									{t("providers:keysForm.vertex.regionDesc2")}
								</FormDescription>
								<FormControl>
									<SecretVarInput placeholder="us-central1 or env.VERTEX_REGION" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>

					{vertexAuthType === "service_account_json" && (
						<FormField
							control={control}
							name={`key.vertex_key_config.auth_credentials`}
							render={({ field }) => (
								<FormItem>
									<FormLabel>{t("providers:keysForm.vertex.authCredentials")}</FormLabel>
									<FormDescription>{t("providers:keysForm.vertex.authCredentialsDesc")}</FormDescription>
									<FormControl>
										<SecretVarInput
											data-testid="apikey-vertex-auth-credentials-input"
											variant="textarea"
											rows={4}
											placeholder='{"type":"service_account","project_id":"your-gcp-project",...} or env.VERTEX_CREDENTIALS'
											inputClassName="font-mono text-sm"
											{...field}
										/>
									</FormControl>
									{isRedacted(field.value?.value ?? "") && (
										<div className="text-muted-foreground mt-1 flex items-center gap-1 text-xs">
											<Info className="h-3 w-3" />
											<span>{t("providers:keysForm.vertex.credentialsStoredSecurely")}</span>
										</div>
									)}
									<FormMessage />
								</FormItem>
							)}
						/>
					)}

					{vertexAuthType === "aws_workload_identity" && (
						<>
							<FormField
								control={control}
								name={`key.vertex_key_config.aws_workload_identity.audience`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers:keysForm.vertex.wifAudience")}</FormLabel>
										<FormDescription>{t("providers:keysForm.vertex.wifAudienceDesc")}</FormDescription>
										<FormControl>
											<SecretVarInput
												data-testid="apikey-vertex-aws-wif-audience-input"
												placeholder="//iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/eks-pool/providers/aws or env.VERTEX_WIF_AUDIENCE"
												inputClassName="font-mono text-sm"
												{...field}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.vertex_key_config.aws_workload_identity.service_account_email`}
								render={({ field }) => (
									<FormItem>
										<div className="flex items-center gap-2">
											<FormLabel>{t("providers:keysForm.vertex.impersonateSa")}</FormLabel>
											<TooltipProvider>
												<Tooltip>
													<TooltipTrigger asChild>
														<span>
															<Info className="text-muted-foreground h-3 w-3" />
														</span>
													</TooltipTrigger>
													<TooltipContent>
														<p>{t("providers:keysForm.vertex.impersonateSaTooltip")}</p>
													</TooltipContent>
												</Tooltip>
											</TooltipProvider>
										</div>
										<FormControl>
											<SecretVarInput
												data-testid="apikey-vertex-aws-wif-service-account-email-input"
												placeholder="vertex-caller@my-project.iam.gserviceaccount.com or env.VERTEX_WIF_SA"
												{...field}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.vertex_key_config.aws_workload_identity.token_lifetime_seconds`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers:keysForm.vertex.tokenLifetime")}</FormLabel>
										<FormDescription>{t("providers:keysForm.vertex.tokenLifetimeDesc")}</FormDescription>
										<FormControl>
											<Input
												data-testid="apikey-vertex-aws-wif-token-lifetime-input"
												type="number"
												min={600}
												max={43200}
												step={60}
												placeholder="3600"
												value={field.value ?? ""}
												onChange={(e) => field.onChange(e.target.value === "" ? undefined : Number(e.target.value))}
												onBlur={field.onBlur}
												name={field.name}
												ref={field.ref}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.vertex_key_config.aws_workload_identity.aws_region`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers:keysForm.vertex.awsRegion")}</FormLabel>
										<FormDescription>{t("providers:keysForm.vertex.awsRegionDesc")}</FormDescription>
										<FormControl>
											<SecretVarInput
												data-testid="apikey-vertex-aws-wif-aws-region-input"
												placeholder="us-east-1 or env.AWS_REGION"
												{...field}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.vertex_key_config.aws_workload_identity.aws_role_arn`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers:keysForm.vertex.awsRoleArn")}</FormLabel>
										<FormDescription>{t("providers:keysForm.vertex.awsRoleArnDesc")}</FormDescription>
										<FormControl>
											<SecretVarInput
												data-testid="apikey-vertex-aws-wif-aws-role-arn-input"
												placeholder="arn:aws:iam::123456789012:role/VertexCaller or env.VERTEX_AWS_ROLE_ARN"
												{...field}
											/>
										</FormControl>
										{isRedacted(field.value?.value ?? "") && (
											<div className="text-muted-foreground mt-1 flex items-center gap-1 text-xs">
												<Info className="h-3 w-3" />
												<span>{t("providers:keysForm.vertex.storedSecurely")}</span>
											</div>
										)}
										<FormMessage />
									</FormItem>
								)}
							/>
						</>
					)}

					{vertexAuthType === "api_key" && (
						<FormField
							control={control}
							name={`key.value`}
							render={({ field }) => (
								<FormItem>
									<FormLabel>{t("providers:keysForm.vertex.apiKeyGeminiOnly")}</FormLabel>
									<FormControl>
										<SecretVarInput
											data-testid="apikey-vertex-api-key-input"
											placeholder={t("providers:keysForm.apiKeyPlaceholder")}
											type="text"
											{...field}
										/>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
					)}
					<FormField
						control={control}
						name="key.vertex_key_config.force_single_region"
						render={({ field }) => (
							<FormItem className="flex flex-row items-center justify-between rounded-sm border p-2">
								<div className="space-y-1.5">
									<FormLabel>{t("providers:keysForm.vertex.forceSingleRegion")}</FormLabel>
									<FormDescription>{t("providers:keysForm.vertex.forceSingleRegionDesc")}</FormDescription>
								</div>
								<FormControl>
									<Switch checked={field.value ?? false} onCheckedChange={field.onChange} />
								</FormControl>
							</FormItem>
						)}
					/>
					{supportsBatchAPI && <BatchAPIFormField control={control} form={form} />}
				</div>
			)}
			{isReplicate && (
				<div className="space-y-4">
					<Separator className="my-6" />
					<FormField
						control={control}
						name="key.replicate_key_config.use_deployments_endpoint"
						render={({ field }) => (
							<FormItem className="flex flex-row items-center justify-between rounded-sm border p-2">
								<div className="space-y-1.5">
									<FormLabel>{t("providers:keysForm.replicate.useDeploymentsEndpoint")}</FormLabel>
									<FormDescription>
										{t("providers:keysForm.replicate.useDeploymentsEndpointDesc1")}
										<strong>{t("providers:keysForm.replicate.useDeploymentsEndpointDesc2")}</strong>
										{t("providers:keysForm.replicate.useDeploymentsEndpointDesc3")}
									</FormDescription>
									<FormMessage />
								</div>
								<FormControl>
									<Switch checked={field.value ?? false} onCheckedChange={field.onChange} />
								</FormControl>
							</FormItem>
						)}
					/>
				</div>
			)}
			{isVLLM && (
				<div className="space-y-4">
					<Separator className="my-6" />
					<FormField
						control={control}
						name="key.vllm_key_config.url"
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers:keysForm.vllm.serverUrl")}</FormLabel>
								<FormDescription>{t("providers:keysForm.vllm.serverUrlDesc")}</FormDescription>
								<FormControl>
									<SecretVarInput data-testid="key-input-vllm-url" placeholder="http://vllm-server:8000" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name="key.vllm_key_config.model_name"
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers:keysForm.vllm.modelName")}</FormLabel>
								<FormDescription>{t("providers:keysForm.vllm.modelNameDesc")}</FormDescription>
								<FormControl>
									<Input data-testid="key-input-vllm-model-name" placeholder="meta-llama/Llama-3-70b-hf" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
				</div>
			)}
			{isDatabricks && (
				<div className="space-y-4">
					<FormField
						control={control}
						name="key.databricks_key_config.workspace_url"
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers:keysForm.databricks.workspaceUrl")}</FormLabel>
								<FormDescription>{t("providers:keysForm.databricks.workspaceUrlDesc")}</FormDescription>
								<FormControl>
									<SecretVarInput
										data-testid="key-input-databricks-workspace-url"
										placeholder="https://dbc-1234abcd-5678.cloud.databricks.com"
										{...field}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name="key.databricks_key_config.api_format"
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers:keysForm.databricks.inferenceSurface")}</FormLabel>
								<FormDescription>{t("providers:keysForm.databricks.inferenceSurfaceDesc")}</FormDescription>
								<Select value={field.value ?? "auto"} onValueChange={field.onChange}>
									<FormControl>
										<SelectTrigger data-testid="key-select-databricks-api-format">
											<SelectValue placeholder={t("providers:databricks.apiFormat.auto")} />
										</SelectTrigger>
									</FormControl>
									<SelectContent>
										<SelectItem value="auto">{t("providers:keysForm.databricks.auto")}</SelectItem>
										<SelectItem value="model_serving">{t("providers:keysForm.databricks.modelServing")}</SelectItem>
										<SelectItem value="ai_gateway">{t("providers:keysForm.databricks.aiGateway")}</SelectItem>
									</SelectContent>
								</Select>
								<FormMessage />
							</FormItem>
						)}
					/>
					<Separator className="my-6" />
					<div className="space-y-2">
						<FormLabel>{t("providers:keysForm.authMethod")}</FormLabel>
						<Tabs
							value={databricksAuthType}
							onValueChange={(v) => {
								setDatabricksAuthType(v as "pat" | "oauth_m2m");
								form.setValue("key.databricks_key_config._auth_type", v, { shouldDirty: true, shouldValidate: true });
								if (v === "oauth_m2m") {
									// The token and the service principal are alternatives, never both.
									form.setValue("key.value", undefined, { shouldDirty: true });
								} else {
									form.setValue("key.databricks_key_config.client_id", undefined, { shouldDirty: true });
									form.setValue("key.databricks_key_config.client_secret", undefined, { shouldDirty: true });
								}
							}}
						>
							<TabsList className="grid w-full grid-cols-2">
								<TabsTrigger data-testid="apikey-databricks-pat-tab" value="pat">
									{t("providers:keysForm.databricks.pat")}
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-databricks-oauth-tab" value="oauth_m2m">
									{t("providers:keysForm.databricks.oauthM2mTab")}
								</TabsTrigger>
							</TabsList>
						</Tabs>
					</div>
					{databricksAuthType === "pat" && (
						<FormField
							control={control}
							name="key.value"
							render={({ field }) => (
								<FormItem>
									<FormLabel>{t("providers:keysForm.databricks.pat")}</FormLabel>
									<FormDescription>{t("providers:keysForm.databricks.patDesc")}</FormDescription>
									<FormControl>
										<SecretVarInput
											data-testid="key-input-databricks-pat"
											placeholder="dapi... or env.DATABRICKS_TOKEN"
											type="text"
											{...field}
										/>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
					)}
					{databricksAuthType === "oauth_m2m" && (
						<>
							<p className="text-muted-foreground text-sm">{t("providers:keysForm.databricks.oauthDesc")}</p>
							<FormField
								control={control}
								name="key.databricks_key_config.client_id"
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers:keysForm.databricks.clientId")}</FormLabel>
										<FormControl>
											<SecretVarInput
												data-testid="key-input-databricks-client-id"
												placeholder={t("providers:keysForm.databricks.clientIdPlaceholder")}
												type="text"
												{...field}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name="key.databricks_key_config.client_secret"
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers:keysForm.databricks.clientSecret")}</FormLabel>
										<FormControl>
											<SecretVarInput
												data-testid="key-input-databricks-client-secret"
												placeholder={t("providers:keysForm.databricks.clientSecretPlaceholder")}
												type="text"
												{...field}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</>
					)}
					<FormField
						control={control}
						name="key.databricks_key_config.forward_gateway_tags"
						render={({ field }) => (
							<FormItem className="flex flex-row items-center justify-between rounded-sm border p-2">
								<div className="space-y-1.5">
									<FormLabel htmlFor="databricks-forward-gateway-tags-switch">{t("providers:keysForm.databricks.forwardTags")}</FormLabel>
									<FormDescription>{t("providers:keysForm.databricks.forwardTagsDesc")}</FormDescription>
								</div>
								<FormControl>
									<Switch id="databricks-forward-gateway-tags-switch" checked={field.value ?? false} onCheckedChange={field.onChange} />
								</FormControl>
							</FormItem>
						)}
					/>
				</div>
			)}
			{isKeylessProvider && (
				<div className="space-y-4">
					<FormField
						control={control}
						name={`key.${isOllama ? "ollama_key_config" : "sgl_key_config"}.url`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers:keysForm.vllm.serverUrl")}</FormLabel>
								<FormDescription>
									{t("providers:keysForm.keyless.serverUrlDesc", {
										server: isOllama ? "Ollama" : "SGLang",
										example: isOllama ? "http://localhost:11434" : "http://localhost:30000",
										envVar: isOllama ? "env.OLLAMA_URL" : "env.SGL_URL",
									})}
								</FormDescription>
								<FormControl>
									<SecretVarInput
										data-testid={`key-input-${isOllama ? "ollama" : "sgl"}-url`}
										placeholder={isOllama ? "http://localhost:11434" : "http://localhost:30000"}
										{...field}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
				</div>
			)}
			{isCloudflare && (
				<div className="space-y-4">
					<FormField
						control={control}
						name="key.cloudflare_key_config.account_id"
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers:oauthKeys.accountId")}</FormLabel>
								<FormDescription>{t("providers:oauthKeys.accountIdDesc")}</FormDescription>
								<FormControl>
									<SecretVarInput data-testid="key-input-cloudflare-account-id" placeholder="env.CLOUDFLARE_ACCOUNT_ID" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
				</div>
			)}
			{isOpenAICompatible && (
				<div className="space-y-4">
					<div className="space-y-2">
						<FormLabel>{t("providers:oauthKeys.authMethod")}</FormLabel>
						<Tabs
							value={openaiAuthType}
							onValueChange={(v) => {
								const next = v as "api_key" | "oauth";
								setOpenaiAuthType(next);
								form.setValue("key.oauth_key_config._auth_type", next, { shouldDirty: true, shouldValidate: true });
								if (next === "oauth") {
									// The static key and the minted bearer are alternatives, never both.
									form.setValue("key.value", undefined, { shouldDirty: true });
									if (!form.getValues("key.oauth_key_config.grant_type")) {
										form.setValue("key.oauth_key_config.grant_type", "client_credentials", { shouldDirty: true });
									}
								}
							}}
						>
							<TabsList className="grid w-full grid-cols-2">
								<TabsTrigger data-testid="apikey-openai-api-key-tab" value="api_key">
									API Key
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-openai-oauth-tab" value="oauth">
									OAuth (minted token)
								</TabsTrigger>
							</TabsList>
						</Tabs>
					</div>
					{openaiAuthType === "api_key" && (
						<FormField
							control={control}
							name="key.value"
							render={({ field }) => (
								<FormItem>
									<FormLabel>{t("providers:oauthKeys.apiKey")}</FormLabel>
									<FormControl>
										<SecretVarInput data-testid="key-input-openai-api-key" placeholder="API Key or env.MY_KEY" type="text" {...field} />
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
					)}
					{openaiAuthType === "oauth" && (
						<>
							<p className="text-muted-foreground text-sm">{t("providers:oauthKeys.flowDescription")}</p>
							<FormField
								control={control}
								name="key.oauth_key_config.grant_type"
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers:oauthKeys.grantType")}</FormLabel>
										<Select value={field.value ?? "client_credentials"} onValueChange={field.onChange}>
											<FormControl>
												<SelectTrigger className="w-full" data-testid="key-select-oauth-grant-type">
													<SelectValue placeholder={t("providers:oauthKeys.grantTypePlaceholder")} />
												</SelectTrigger>
											</FormControl>
											<SelectContent>
												<SelectItem value="client_credentials">{t("providers:oauthKeys.grantClientCredentials")}</SelectItem>
												<SelectItem value="jwt_bearer">{t("providers:oauthKeys.grantJwtBearer")}</SelectItem>
											</SelectContent>
										</Select>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name="key.oauth_key_config.token_url"
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers:oauthKeys.tokenUrl")}</FormLabel>
										<FormDescription>{t("providers:oauthKeys.tokenUrlDesc")}</FormDescription>
										<FormControl>
											<SecretVarInput
												data-testid="key-input-oauth-token-url"
												placeholder="https://idp.example.com/oauth2/token or env.IDP_TOKEN_URL"
												{...field}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							{oauthGrantType === "client_credentials" && (
								<>
									<FormField
										control={control}
										name="key.oauth_key_config.client_id"
										render={({ field }) => (
											<FormItem>
												<FormLabel>{t("providers:oauthKeys.clientId")}</FormLabel>
												<FormControl>
													<SecretVarInput data-testid="key-input-oauth-client-id" placeholder="Client ID or env.IDP_CLIENT_ID" {...field} />
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
									<FormField
										control={control}
										name="key.oauth_key_config.client_secret"
										render={({ field }) => (
											<FormItem>
												<FormLabel>{t("providers:oauthKeys.clientSecret")}</FormLabel>
												<FormControl>
													<SecretVarInput
														data-testid="key-input-oauth-client-secret"
														placeholder="Client secret or env.IDP_CLIENT_SECRET"
														{...field}
													/>
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
									<FormField
										control={control}
										name="key.oauth_key_config.auth_style"
										render={({ field }) => (
											<FormItem>
												<FormLabel>{t("providers:oauthKeys.credentialPlacement")}</FormLabel>
												<FormDescription>
													Where the token request carries the client ID and secret. Header (HTTP Basic) is the standard; some providers only
													accept them as form fields.
												</FormDescription>
												<Select value={field.value ?? "header"} onValueChange={field.onChange}>
													<FormControl>
														<SelectTrigger className="w-full" data-testid="key-select-oauth-auth-style">
															<SelectValue placeholder="Header" />
														</SelectTrigger>
													</FormControl>
													<SelectContent>
														<SelectItem value="header">{t("providers:oauthKeys.placementBasic")}</SelectItem>
														<SelectItem value="body">{t("providers:oauthKeys.placementBody")}</SelectItem>
													</SelectContent>
												</Select>
												<FormMessage />
											</FormItem>
										)}
									/>
								</>
							)}
							{oauthGrantType === "jwt_bearer" && (
								<>
									<FormField
										control={control}
										name="key.oauth_key_config.private_key"
										render={({ field }) => (
											<FormItem>
												<FormLabel>{t("providers:oauthKeys.privateKey")}</FormLabel>
												<FormDescription>{t("providers:oauthKeys.privateKeyDesc")}</FormDescription>
												<FormControl>
													<SecretVarInput
														data-testid="key-input-oauth-private-key"
														variant="textarea"
														rows={4}
														placeholder="-----BEGIN PRIVATE KEY----- or env.IDP_PRIVATE_KEY"
														{...field}
													/>
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
									<FormField
										control={control}
										name="key.oauth_key_config.issuer"
										render={({ field }) => (
											<FormItem>
												<FormLabel>{t("providers:oauthKeys.issuer")}</FormLabel>
												<FormDescription>{t("providers:oauthKeys.issuerDesc")}</FormDescription>
												<FormControl>
													<Input data-testid="key-input-oauth-issuer" placeholder="client-id" {...field} value={field.value ?? ""} />
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
									<FormField
										control={control}
										name="key.oauth_key_config.subject"
										render={({ field }) => (
											<FormItem>
												<FormLabel>{t("providers:oauthKeys.subject")}</FormLabel>
												<FormDescription>{t("providers:oauthKeys.subjectDesc")}</FormDescription>
												<FormControl>
													<Input
														data-testid="key-input-oauth-subject"
														placeholder="service-account@example"
														{...field}
														value={field.value ?? ""}
													/>
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
									<FormField
										control={control}
										name="key.oauth_key_config.audience"
										render={({ field }) => (
											<FormItem>
												<FormLabel>{t("providers:oauthKeys.audience")}</FormLabel>
												<FormDescription>{t("providers:oauthKeys.audienceDesc")}</FormDescription>
												<FormControl>
													<Input
														data-testid="key-input-oauth-audience"
														placeholder="https://idp.example.com/oauth2/token"
														{...field}
														value={field.value ?? ""}
													/>
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
									<FormField
										control={control}
										name="key.oauth_key_config.key_id"
										render={({ field }) => (
											<FormItem>
												<FormLabel>{t("providers:oauthKeys.keyId")}</FormLabel>
												<FormDescription>{t("providers:oauthKeys.keyIdDesc")}</FormDescription>
												<FormControl>
													<Input data-testid="key-input-oauth-key-id" placeholder="kid-1" {...field} value={field.value ?? ""} />
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
									<FormField
										control={control}
										name="key.oauth_key_config.signing_algorithm"
										render={({ field }) => (
											<FormItem>
												<FormLabel>{t("providers:oauthKeys.signingAlgorithm")}</FormLabel>
												<Select value={field.value ?? "RS256"} onValueChange={field.onChange}>
													<FormControl>
														<SelectTrigger className="w-full" data-testid="key-select-oauth-signing-algorithm">
															<SelectValue placeholder="RS256" />
														</SelectTrigger>
													</FormControl>
													<SelectContent>
														{OAuthSigningAlgorithms.map((alg) => (
															<SelectItem key={alg} value={alg}>
																{alg}
															</SelectItem>
														))}
													</SelectContent>
												</Select>
												<FormMessage />
											</FormItem>
										)}
									/>
									<FormField
										control={control}
										name="key.oauth_key_config.assertion_lifetime_seconds"
										render={({ field }) => (
											<FormItem>
												<FormLabel>{t("providers:oauthKeys.assertionLifetime")}</FormLabel>
												<FormDescription>{t("providers:oauthKeys.assertionLifetimeDesc")}</FormDescription>
												<FormControl>
													<Input
														data-testid="key-input-oauth-assertion-lifetime"
														type="number"
														min={0}
														max={3600}
														placeholder="300"
														{...field}
														value={field.value ?? ""}
														onChange={(e) => field.onChange(e.target.value === "" ? undefined : Number(e.target.value))}
													/>
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
								</>
							)}
							<FormField
								control={control}
								name="key.oauth_key_config.scopes"
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers:oauthKeys.scopes")}</FormLabel>
										<FormControl>
											<TagInput
												data-testid="key-input-oauth-scopes"
												placeholder={t("providers:keysForm.azure.scopesPlaceholder")}
												value={field.value ?? []}
												onValueChange={field.onChange}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							{oauthGrantType === "client_credentials" && (
								<FormField
									control={control}
									name="key.oauth_key_config.audience"
									render={({ field }) => (
										<FormItem>
											<FormLabel>{t("providers:oauthKeys.audienceOptional")}</FormLabel>
											<FormDescription>{t("providers:oauthKeys.audienceOptionalDesc")}</FormDescription>
											<FormControl>
												<Input
													data-testid="key-input-oauth-cc-audience"
													placeholder="api://my-llm-gateway"
													{...field}
													value={field.value ?? ""}
												/>
											</FormControl>
											<FormMessage />
										</FormItem>
									)}
								/>
							)}
						</>
					)}
				</div>
			)}
			{isGithubCopilot && (
				<div className="space-y-4">
					<Separator />
					<div className="bg-muted/50 flex items-start gap-2 rounded-md border p-3">
						<Info className="text-muted-foreground mt-0.5 h-4 w-4 shrink-0" />
						<p className="text-muted-foreground text-sm">
							{t("providers:keysForm.copilot.noteA")} <strong>{t("providers:keysForm.copilot.noteB")}</strong>{" "}
							{t("providers:keysForm.copilot.noteC")} <strong>{t("providers:keysForm.copilot.noteD")}</strong>
							{t("providers:keysForm.copilot.noteE")} <strong>{t("providers:keysForm.copilot.noteF")}</strong>
							{t("providers:keysForm.copilot.noteG")}{" "}
							<a
								href="https://docs.github.com/en/copilot/how-tos/copilot-sdk/auth/server-to-server-tokens"
								target="_blank"
								rel="noopener noreferrer"
								className="text-primary hover:underline"
								data-testid="copilot-docs-link-server-to-server"
							>
								{t("providers:keysForm.copilot.setUpGithubApp")}
							</a>
							{` ${t("providers:keysForm.copilot.or")} `}
							<a
								href="https://docs.github.com/en/copilot/how-tos/copilot-sdk/authenticate-copilot-sdk/authenticate-copilot-sdk"
								target="_blank"
								rel="noopener noreferrer"
								className="text-primary hover:underline"
								data-testid="copilot-docs-link-api-token"
							>
								{t("providers:keysForm.copilot.getApiToken")}
							</a>
							.
						</p>
					</div>
					<div className="space-y-1.5">
						{/* Label, not FormLabel: this heads a section rather than labelling one
						    control, so there is no FormItem id for htmlFor to point at. */}
						<Label>{t("providers:keysForm.copilot.appCredentials")}</Label>
						<p className="text-muted-foreground text-sm">{t("providers:keysForm.copilot.appCredentialsNote")}</p>
					</div>
					<FormField
						control={control}
						name="key.github_copilot_key_config.app_id"
						render={({ field }) => (
							<FormItem>
								<FormLabel>
									{t("providers:keysForm.copilot.appId")} {copilotAppSuffix}
								</FormLabel>
								<FormDescription>
									{t("providers:keysForm.copilot.appIdDesc1")}{" "}
									<a
										href="https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/registering-a-github-app"
										target="_blank"
										rel="noopener noreferrer"
										className="text-primary hover:underline"
										data-testid="copilot-docs-link-create-app"
									>
										{t("providers:keysForm.copilot.createGithubApp")}
									</a>
								</FormDescription>
								<FormControl>
									<SecretVarInput data-testid="key-input-copilot-app-id" placeholder="123456 or env.COPILOT_APP_ID" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name="key.github_copilot_key_config.installation_id"
						render={({ field }) => (
							<FormItem>
								<FormLabel>
									{t("providers:keysForm.copilot.installationId")} {copilotAppSuffix}
								</FormLabel>
								<FormDescription>
									{t("providers:keysForm.copilot.installationIdDesc1")}{" "}
									<a
										href="https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-an-installation-access-token-for-a-github-app"
										target="_blank"
										rel="noopener noreferrer"
										className="text-primary hover:underline"
										data-testid="copilot-docs-link-installation-id"
									>
										{t("providers:keysForm.copilot.findInstallationId")}
									</a>
								</FormDescription>
								<FormControl>
									<SecretVarInput
										data-testid="key-input-copilot-installation-id"
										placeholder="87654321 or env.COPILOT_INSTALLATION_ID"
										{...field}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name="key.github_copilot_key_config.repository_id"
						render={({ field }) => (
							<FormItem>
								<FormLabel>
									{t("providers:keysForm.copilot.repositoryId")} {copilotAppSuffix}
								</FormLabel>
								<FormDescription>{t("providers:keysForm.copilot.repositoryIdDesc")}</FormDescription>
								<FormControl>
									<SecretVarInput
										data-testid="key-input-copilot-repository-id"
										placeholder="999000111 or env.COPILOT_REPOSITORY_ID"
										{...field}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name="key.github_copilot_key_config.private_key"
						render={({ field }) => (
							<FormItem>
								<FormLabel>
									{t("providers:keysForm.copilot.privateKey")} {copilotAppSuffix}
								</FormLabel>
								<FormDescription>{t("providers:keysForm.copilot.privateKeyDesc")}</FormDescription>
								<FormControl>
									<SecretVarInput
										data-testid="key-input-copilot-private-key"
										variant="textarea"
										rows={4}
										placeholder="-----BEGIN RSA PRIVATE KEY----- or env.COPILOT_PRIVATE_KEY"
										{...field}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name="key.github_copilot_key_config.github_domain"
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers:keysForm.copilot.gheDomain")}</FormLabel>
								<FormDescription>{t("providers:keysForm.copilot.gheDomainDesc")}</FormDescription>
								<FormControl>
									<SecretVarInput data-testid="key-input-copilot-github-domain" placeholder="acme.ghe.com" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
				</div>
			)}
			{(isSGL || isDeepseek || isFireworks || isVLLM) && (
				<div className="space-y-4">
					<FormField
						control={control}
						name="key.use_anthropic_endpoints"
						render={({ field }) => (
							<FormItem className="flex flex-row items-center justify-between rounded-sm border p-2">
								<div className="space-y-1.5">
									<FormLabel htmlFor="use-anthropic-endpoints-alias-override-switch">
										{t("providers:keysForm.anthropicEndpoints.label")}
									</FormLabel>
									<FormDescription>{t("providers:keysForm.anthropicEndpoints.desc")}</FormDescription>
								</div>
								<FormControl>
									<Switch
										id="use-anthropic-endpoints-alias-override-switch"
										checked={field.value ?? false}
										onCheckedChange={field.onChange}
									/>
								</FormControl>
							</FormItem>
						)}
					/>
				</div>
			)}
			{isBedrock && (
				<div className="space-y-4">
					<FormField
						control={control}
						name="key.use_openai_endpoints"
						render={({ field }) => (
							<FormItem className="flex flex-row items-center justify-between rounded-sm border p-2">
								<div className="space-y-1.5">
									<FormLabel htmlFor="use-openai-endpoints-switch">{t("providers:keysForm.bedrock.useOpenaiEndpoints")}</FormLabel>
									<FormDescription>{t("providers:keysForm.bedrock.useOpenaiEndpointsDesc")}</FormDescription>
								</div>
								<FormControl>
									<Switch
										id="use-openai-endpoints-switch"
										data-testid="key-switch-bedrock-use-openai-endpoints"
										checked={field.value ?? false}
										onCheckedChange={field.onChange}
									/>
								</FormControl>
							</FormItem>
						)}
					/>
					<Separator className="my-6" />
					<div className="space-y-2">
						<FormLabel>{t("providers:keysForm.authMethod")}</FormLabel>
						<Tabs
							value={bedrockAuthType}
							onValueChange={(v) => {
								setBedrockAuthType(v as "iam_role" | "explicit" | "api_key");
								form.setValue("key.bedrock_key_config._auth_type", v, { shouldDirty: true, shouldValidate: true });
								if (v === "iam_role") {
									// Clear explicit credentials and API key when switching to IAM Role
									form.setValue("key.bedrock_key_config.access_key", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_key_config.secret_key", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_key_config.session_token", undefined, { shouldDirty: true });
									form.setValue("key.value", undefined, { shouldDirty: true });
								} else if (v === "explicit") {
									// Clear API key when switching to Explicit Credentials
									form.setValue("key.value", undefined, { shouldDirty: true });
								} else if (v === "api_key") {
									// Clear AWS credentials and assume-role fields when switching to API Key
									form.setValue("key.bedrock_key_config.access_key", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_key_config.secret_key", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_key_config.session_token", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_key_config.role_arn", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_key_config.external_id", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_key_config.session_name", undefined, { shouldDirty: true });
								}
							}}
						>
							<TabsList className="flex w-full justify-start">
								<TabsTrigger data-testid="apikey-bedrock-iam-role-tab" value="iam_role">
									{t("providers:keysForm.bedrock.iamRoleTab")}
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-bedrock-explicit-credentials-tab" value="explicit">
									{t("providers:keysForm.bedrock.explicitTab")}
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-bedrock-api-key-tab" value="api_key">
									{t("providers:keysForm.apiKey")}
								</TabsTrigger>
							</TabsList>
						</Tabs>
						{bedrockAuthType === "iam_role" && (
							<p className="text-muted-foreground text-sm">{t("providers:keysForm.bedrock.iamRoleDesc")}</p>
						)}
						{bedrockAuthType === "api_key" && <p className="text-muted-foreground text-sm">{t("providers:keysForm.bedrock.bearerDesc")}</p>}
					</div>

					{bedrockAuthType === "explicit" && (
						<>
							<FormField
								control={control}
								name={`key.bedrock_key_config.access_key`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers:keysForm.bedrock.accessKey")}</FormLabel>
										<FormControl>
											<SecretVarInput placeholder="your-aws-access-key or env.AWS_ACCESS_KEY_ID" {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.bedrock_key_config.secret_key`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers:keysForm.bedrock.secretKey")}</FormLabel>
										<FormControl>
											<SecretVarInput placeholder="your-aws-secret-key or env.AWS_SECRET_ACCESS_KEY" {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.bedrock_key_config.session_token`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers:keysForm.bedrock.sessionToken")}</FormLabel>
										<FormControl>
											<SecretVarInput placeholder="your-aws-session-token or env.AWS_SESSION_TOKEN" {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</>
					)}

					{bedrockAuthType === "api_key" && (
						<FormField
							control={control}
							name={`key.value`}
							render={({ field }) => (
								<FormItem>
									<FormLabel>{t("providers:keysForm.apiKey")}</FormLabel>
									<FormControl>
										<SecretVarInput
											data-testid="apikey-bedrock-api-key-input"
											placeholder="API Key or env.BEDROCK_API_KEY"
											type="text"
											{...field}
										/>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
					)}

					<FormField
						control={control}
						name={`key.bedrock_key_config.region`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers:keysForm.vertex.region")}</FormLabel>
								<FormControl>
									<SecretVarInput placeholder="us-east-1 or env.AWS_REGION" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name={`key.bedrock_key_config.project_id`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers:keysForm.bedrock.mantleProjectId")}</FormLabel>
								<FormDescription>{t("providers:keysForm.bedrock.mantleProjectIdDesc")}</FormDescription>
								<FormControl>
									<SecretVarInput
										data-testid="apikey-bedrock-project-id-input"
										placeholder="proj_xxxxxxxx or env.BEDROCK_PROJECT_ID"
										{...field}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					{bedrockAuthType !== "api_key" && (
						<>
							<FormField
								control={control}
								name={`key.bedrock_key_config.role_arn`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers:keysForm.bedrock.assumeRoleArn")}</FormLabel>
										<FormDescription>{t("providers:keysForm.bedrock.assumeRoleArnDesc")}</FormDescription>
										<FormControl>
											<SecretVarInput
												data-testid="apikey-bedrock-role-arn-input"
												placeholder="arn:aws:iam::123456789:role/MyRole or env.AWS_ROLE_ARN"
												{...field}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.bedrock_key_config.external_id`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers:keysForm.bedrock.externalId")}</FormLabel>
										<FormDescription>{t("providers:keysForm.bedrock.externalIdDesc")}</FormDescription>
										<FormControl>
											<SecretVarInput
												data-testid="apikey-bedrock-external-id-input"
												placeholder="external-id or env.AWS_EXTERNAL_ID"
												{...field}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.bedrock_key_config.session_name`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers:keysForm.bedrock.sessionName")}</FormLabel>
										<FormDescription>{t("providers:keysForm.bedrock.sessionNameDesc")}</FormDescription>
										<FormControl>
											<SecretVarInput
												data-testid="apikey-bedrock-session-name-input"
												placeholder="bifrost-session or env.AWS_SESSION_NAME"
												{...field}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</>
					)}
					<FormField
						control={control}
						name={`key.bedrock_key_config.arn`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers:keysForm.bedrock.arn")}</FormLabel>
								<FormControl>
									<SecretVarInput placeholder="arn:aws:bedrock:us-east-1:123:inference-profile or env.AWS_ARN" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					{supportsBatchAPI && (
						<FormField
							control={control}
							name={`key.bedrock_key_config.batch_role_arn`}
							render={({ field }) => (
								<FormItem>
									<FormLabel>{t("providers:keysForm.bedrock.batchRoleArn")}</FormLabel>
									<FormDescription>{t("providers:keysForm.bedrock.batchRoleArnDesc")}</FormDescription>
									<FormControl>
										<SecretVarInput
											data-testid="apikey-bedrock-batch-role-arn-input"
											placeholder="arn:aws:iam::123456789:role/BatchRole or env.AWS_BATCH_ROLE_ARN"
											{...field}
										/>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
					)}
					{supportsBatchAPI && <BatchAPIFormField control={control} form={form} />}
					<VPCEndpointsFormField control={control} configKey="key.bedrock_key_config" services={BEDROCK_VPC_ENDPOINT_SERVICES} />
				</div>
			)}

			{isBedrockMantle && (
				<div className="space-y-4">
					<Separator className="my-6" />
					<div className="space-y-2">
						<FormLabel>{t("providers:keysForm.authMethod")}</FormLabel>
						<Tabs
							value={bedrockMantleAuthType}
							onValueChange={(v) => {
								setBedrockMantleAuthType(v as "iam_role" | "explicit" | "api_key");
								form.setValue("key.bedrock_mantle_key_config._auth_type", v, { shouldDirty: true, shouldValidate: true });
								if (v === "iam_role") {
									// Clear explicit credentials and API key when switching to IAM Role
									form.setValue("key.bedrock_mantle_key_config.access_key", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_mantle_key_config.secret_key", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_mantle_key_config.session_token", undefined, { shouldDirty: true });
									form.setValue("key.value", undefined, { shouldDirty: true });
								} else if (v === "explicit") {
									// Clear API key when switching to Explicit Credentials
									form.setValue("key.value", undefined, { shouldDirty: true });
								} else if (v === "api_key") {
									// Clear AWS credentials and assume-role fields when switching to API Key
									form.setValue("key.bedrock_mantle_key_config.access_key", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_mantle_key_config.secret_key", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_mantle_key_config.session_token", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_mantle_key_config.role_arn", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_mantle_key_config.external_id", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_mantle_key_config.session_name", undefined, { shouldDirty: true });
								}
							}}
						>
							<TabsList className="flex w-full justify-start">
								<TabsTrigger data-testid="apikey-bedrock-mantle-iam-role-tab" value="iam_role">
									{t("providers:keysForm.bedrock.iamRoleTab")}
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-bedrock-mantle-explicit-credentials-tab" value="explicit">
									{t("providers:keysForm.bedrock.explicitTab")}
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-bedrock-mantle-api-key-tab" value="api_key">
									{t("providers:keysForm.apiKey")}
								</TabsTrigger>
							</TabsList>
						</Tabs>
						{bedrockMantleAuthType === "iam_role" && (
							<p className="text-muted-foreground text-sm">{t("providers:keysForm.bedrock.iamRoleDesc")}</p>
						)}
						{bedrockMantleAuthType === "api_key" && (
							<p className="text-muted-foreground text-sm">{t("providers:keysForm.bedrock.mantleBearerDesc")}</p>
						)}
					</div>

					{bedrockMantleAuthType === "explicit" && (
						<>
							<FormField
								control={control}
								name={`key.bedrock_mantle_key_config.access_key`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers:keysForm.bedrock.accessKey")}</FormLabel>
										<FormControl>
											<SecretVarInput placeholder="your-aws-access-key or env.AWS_ACCESS_KEY_ID" {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.bedrock_mantle_key_config.secret_key`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers:keysForm.bedrock.secretKey")}</FormLabel>
										<FormControl>
											<SecretVarInput placeholder="your-aws-secret-key or env.AWS_SECRET_ACCESS_KEY" {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.bedrock_mantle_key_config.session_token`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers:keysForm.bedrock.sessionToken")}</FormLabel>
										<FormControl>
											<SecretVarInput placeholder="your-aws-session-token or env.AWS_SESSION_TOKEN" {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</>
					)}

					{bedrockMantleAuthType === "api_key" && (
						<FormField
							control={control}
							name={`key.value`}
							render={({ field }) => (
								<FormItem>
									<FormLabel>{t("providers:keysForm.apiKey")}</FormLabel>
									<FormControl>
										<SecretVarInput
											data-testid="apikey-bedrock-mantle-api-key-input"
											placeholder="API Key or env.BEDROCK_MANTLE_API_KEY"
											type="text"
											{...field}
										/>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
					)}

					<FormField
						control={control}
						name={`key.bedrock_mantle_key_config.region`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers:keysForm.vertex.region")}</FormLabel>
								<FormControl>
									<SecretVarInput placeholder="us-east-1 or env.AWS_REGION" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>

					<FormField
						control={control}
						name={`key.bedrock_mantle_key_config.project_id`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers:keysForm.bedrock.projectId")}</FormLabel>
								<FormDescription>{t("providers:keysForm.bedrock.projectIdDesc")}</FormDescription>
								<FormControl>
									<SecretVarInput
										data-testid="apikey-bedrock-mantle-project-id-input"
										placeholder="proj_xxxxxxxx or env.BEDROCK_PROJECT_ID"
										{...field}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>

					{bedrockMantleAuthType !== "api_key" && (
						<>
							<FormField
								control={control}
								name={`key.bedrock_mantle_key_config.role_arn`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers:keysForm.bedrock.assumeRoleArn")}</FormLabel>
										<FormDescription>{t("providers:keysForm.bedrock.assumeRoleArnDesc")}</FormDescription>
										<FormControl>
											<SecretVarInput placeholder="arn:aws:iam::123456789:role/MyRole or env.AWS_ROLE_ARN" {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.bedrock_mantle_key_config.external_id`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers:keysForm.bedrock.externalId")}</FormLabel>
										<FormDescription>{t("providers:keysForm.bedrock.externalIdDescPeriod")}</FormDescription>
										<FormControl>
											<SecretVarInput placeholder="external-id or env.AWS_EXTERNAL_ID" {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.bedrock_mantle_key_config.session_name`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers:keysForm.bedrock.sessionName")}</FormLabel>
										<FormDescription>{t("providers:keysForm.bedrock.sessionNameDescPeriod")}</FormDescription>
										<FormControl>
											<SecretVarInput placeholder="bifrost-session or env.AWS_SESSION_NAME" {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</>
					)}
					<VPCEndpointsFormField
						control={control}
						configKey="key.bedrock_mantle_key_config"
						services={BEDROCK_VPC_ENDPOINT_SERVICES.filter((s) => s.name === "mantle")}
					/>
				</div>
			)}
		</div>
	);
}