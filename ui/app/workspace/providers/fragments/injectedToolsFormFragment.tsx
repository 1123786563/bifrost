import { MCPClientSelector } from "@/components/entitySelectors/mcpClientSelector";
import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { getErrorMessage, setProviderFormDirtyState, useAppDispatch, useGetMCPClientsQuery, useLazyGetMCPClientsQuery } from "@/lib/store";
import { useUpdateProviderMutation } from "@/lib/store/apis/providersApi";
import type { ModelProvider } from "@/lib/types/config";
import { injectedWebSearchFormSchema, type InjectedWebSearchFormSchema } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect, useState } from "react";
import { useForm, type Resolver } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { buildProviderUpdatePayload } from "../views/utils";
import {
	hydratedValues,
	lookupSavedClient,
	nextSavedClientState,
	pickersDisabled,
	savedFormValues,
	startedSavedClientState,
	toolMenuState,
	type SavedClientState,
} from "./injectedToolsForm.utils";

const SAVED_CLIENT_PAGE_SIZE = 50;

const TOOL_PLACEHOLDER_KEYS: Record<ReturnType<typeof toolMenuState>, string> = {
	"no-server": "providers:webSearch.placeholders.noServer",
	loading: "providers:webSearch.placeholders.loading",
	error: "providers:webSearch.placeholders.error",
	empty: "providers:webSearch.placeholders.empty",
	ready: "providers:webSearch.placeholders.ready",
};

interface InjectedToolsFormFragmentProps {
	provider: ModelProvider;
}

// The provider stores the MCP client by name, while the client picker works by id. The
// list search matches names by substring, so it pages on until the exact name turns up.
// It reports the name the id was resolved for, so callers never pair an id with another name,
// and whether the lookup failed or found the server gone; retry runs it again.
function useSavedClientId(clientName: string): SavedClientState & { retry: () => void } {
	const [fetchClients] = useLazyGetMCPClientsQuery();
	const [resolved, setResolved] = useState<SavedClientState>({ name: "", id: "", status: "idle" });
	const [attempt, setAttempt] = useState(0);
	useEffect(() => {
		if (clientName === "") return;
		let cancelled = false;
		setResolved((previous) => startedSavedClientState(clientName, previous));
		lookupSavedClient(
			(offset) => fetchClients({ search: clientName, limit: SAVED_CLIENT_PAGE_SIZE, offset }, true).unwrap(),
			clientName,
			SAVED_CLIENT_PAGE_SIZE,
			() => cancelled,
		).then((result) => {
			const next = nextSavedClientState(clientName, result);
			if (!cancelled && next) setResolved(next);
		});
		return () => {
			cancelled = true;
		};
	}, [clientName, fetchClients, attempt]);
	return { ...resolved, retry: () => setAttempt((n) => n + 1) };
}

export function InjectedToolsFormFragment({ provider }: InjectedToolsFormFragmentProps) {
	const { t } = useTranslation(["common", "providers"]);
	const dispatch = useAppDispatch();
	const hasUpdateProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const [updateProvider, { isLoading: isUpdatingProvider }] = useUpdateProviderMutation();

	const saved = provider.injected_tools?.web_search;
	const savedClientName = saved?.mcp_client_name ?? "";
	const savedToolName = saved?.tool_name ?? "";
	const resolvedClient = useSavedClientId(savedClientName);
	const savedClientId = resolvedClient.name === savedClientName ? resolvedClient.id : "";
	const savedClientStatus = resolvedClient.name === savedClientName ? resolvedClient.status : "idle";
	const lockPickers = pickersDisabled(hasUpdateProviderAccess, isUpdatingProvider);

	const form = useForm<InjectedWebSearchFormSchema, any, InjectedWebSearchFormSchema>({
		resolver: zodResolver(injectedWebSearchFormSchema) as Resolver<InjectedWebSearchFormSchema, any, InjectedWebSearchFormSchema>,
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: { mcp_client_id: savedClientId, mcp_client_name: savedClientName, tool_name: savedToolName },
	});

	useEffect(() => {
		dispatch(setProviderFormDirtyState(form.formState.isDirty));
	}, [form.formState.isDirty, dispatch]);

	// A different provider, or a saved setting that changed, replaces the form. The resolved
	// client id is kept when it belongs to the saved client, since the lookup will not run
	// again for an unchanged name.
	useEffect(() => {
		form.reset(savedFormValues(savedClientName, savedToolName, resolvedClient));
		// eslint-disable-next-line react-hooks/exhaustive-deps -- a late-resolving id is handled by the hydration effect below
	}, [form, provider.name, savedClientName, savedToolName]);

	// The saved client's id resolves after the form opens; load it only into an untouched form.
	useEffect(() => {
		const values = hydratedValues(form.formState.isDirty, {
			mcp_client_id: savedClientId,
			mcp_client_name: savedClientName,
			tool_name: savedToolName,
		});
		if (values && savedClientId !== "") form.reset(values);
		// eslint-disable-next-line react-hooks/exhaustive-deps -- runs when the id resolves, not on every edit
	}, [savedClientId]);

	const clientId = form.watch("mcp_client_id");
	const toolsQuery = useGetMCPClientsQuery({ server: clientId, limit: 1 }, { skip: clientId === "" });
	const selectedClient = toolsQuery.data?.clients?.[0];
	const tools = selectedClient?.tools ?? [];
	const menuState = toolMenuState(clientId, toolsQuery, tools.length);

	// The picker reports only the id; the provider config stores the name.
	useEffect(() => {
		if (
			selectedClient &&
			selectedClient.config.client_id === clientId &&
			form.getValues("mcp_client_name") !== selectedClient.config.name
		) {
			form.setValue("mcp_client_name", selectedClient.config.name, { shouldDirty: true, shouldValidate: true });
		}
	}, [selectedClient, clientId, form]);

	// Save always writes a complete selection (the schema guarantees it); only Remove
	// passes null, which clears the provider's injected tools.
	const save = (data: InjectedWebSearchFormSchema | null) => {
		const injectedTools = data ? { web_search: { mcp_client_name: data.mcp_client_name, tool_name: data.tool_name } } : null;
		updateProvider(buildProviderUpdatePayload(provider, { injected_tools: injectedTools }))
			.unwrap()
			.then(() => {
				toast.success(injectedTools ? t("providers:webSearch.updatedToast") : t("providers:webSearch.removedToast"));
				form.reset(data ?? { mcp_client_id: "", mcp_client_name: "", tool_name: "" });
			})
			.catch((err) => {
				toast.error(t("providers:webSearch.updateFailed"), { description: getErrorMessage(err) });
			});
	};

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(save)} className="space-y-6 px-4 md:px-6" data-testid="provider-config-web-search-content">
				<p className="text-muted-foreground text-xs">{t("providers:webSearch.description")}</p>
				<FormField
					control={form.control}
					name="mcp_client_id"
					render={({ field }) => (
						<FormItem>
							<FormLabel>{t("providers:webSearch.mcpServer")}</FormLabel>
							<FormControl>
								<div data-testid="provider-web-search-client-select">
									<MCPClientSelector
										value={field.value}
										onChange={(value) => {
											field.onChange(value);
											form.setValue("mcp_client_name", "", { shouldDirty: true });
											form.setValue("tool_name", "", { shouldDirty: true, shouldValidate: true });
										}}
										disabled={lockPickers}
										fallbackOption={savedClientId ? { value: savedClientId, label: savedClientName } : null}
									/>
								</div>
							</FormControl>
							{savedClientStatus === "loading" && !form.formState.isDirty && (
								<p className="text-muted-foreground text-xs" data-testid="provider-web-search-client-loading">
									{t("providers:webSearch.loadingSaved", { name: savedClientName })}
								</p>
							)}
							{savedClientStatus === "missing" && !form.formState.isDirty && (
								<p className="text-destructive text-xs" data-testid="provider-web-search-client-missing">
									{t("providers:webSearch.savedMissing", { name: savedClientName })}
								</p>
							)}
							{savedClientStatus === "error" && !form.formState.isDirty && (
								<div className="text-destructive flex items-center gap-2 text-xs" data-testid="provider-web-search-client-error">
									{t("providers:webSearch.savedError", { name: savedClientName })}
									<Button
										type="button"
										variant="outline"
										size="sm"
										onClick={resolvedClient.retry}
										data-testid="provider-web-search-client-retry"
									>
										{t("common:actions.retry")}
									</Button>
								</div>
							)}
							<FormMessage />
						</FormItem>
					)}
				/>
				<FormField
					control={form.control}
					name="tool_name"
					render={({ field }) => (
						<FormItem>
							<FormLabel>{t("providers:webSearch.tool")}</FormLabel>
							<Select value={field.value} onValueChange={field.onChange} disabled={lockPickers || menuState !== "ready"}>
								<FormControl>
									<SelectTrigger className="w-full" data-testid="provider-web-search-tool-select">
										<SelectValue placeholder={t(TOOL_PLACEHOLDER_KEYS[menuState])} />
									</SelectTrigger>
								</FormControl>
								<SelectContent>
									{tools.map((tool) => (
										<SelectItem key={tool.name} value={tool.name} data-testid={`provider-web-search-tool-option-${tool.name}`}>
											{tool.name}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
							{menuState === "error" && (
								<div className="text-destructive flex items-center gap-2 text-xs" data-testid="provider-web-search-tool-error">
									{t("providers:webSearch.toolsError")}
									<Button
										type="button"
										variant="outline"
										size="sm"
										onClick={() => toolsQuery.refetch()}
										data-testid="provider-web-search-tool-retry"
									>
										{t("common:actions.retry")}
									</Button>
								</div>
							)}
							<FormMessage />
						</FormItem>
					)}
				/>

				<div className="flex justify-end space-x-2 pb-6">
					{saved && (
						<Button
							type="button"
							variant="outline"
							data-testid="provider-web-search-remove-btn"
							disabled={!hasUpdateProviderAccess || isUpdatingProvider}
							onClick={() => save(null)}
						>
							{t("common:actions.remove")}
						</Button>
					)}
					<Button
						type="submit"
						data-testid="provider-web-search-save-btn"
						disabled={!form.formState.isDirty || !form.formState.isValid || !hasUpdateProviderAccess || isUpdatingProvider}
						isLoading={isUpdatingProvider}
					>
						{t("providers:webSearch.save")}
					</Button>
				</div>
			</form>
		</Form>
	);
}