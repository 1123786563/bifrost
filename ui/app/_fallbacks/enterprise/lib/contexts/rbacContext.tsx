import { createContext, useContext } from "react";

// RBAC Resource Names (must match backend definitions)
export enum RbacResource {
	GuardrailsConfig = "GuardrailsConfig",
	GuardrailsProviders = "GuardrailsProviders",
	GuardrailRules = "GuardrailRules",
	UserProvisioning = "UserProvisioning",
	Cluster = "Cluster",
	Settings = "Settings",
	Users = "Users",
	Logs = "Logs",
	Observability = "Observability",
	Dashboard = "Dashboard",
	VirtualKeys = "VirtualKeys",
	ModelProvider = "ModelProvider",
	Plugins = "Plugins",
	MCPGateway = "MCPGateway",
	AgentGateway = "AgentGateway",
	AgentLogs = "AgentLogs",
	MCPToolGroups = "MCPToolGroups",
	VirtualMCPs = "VirtualMCPs",
	MCPLogs = "MCPLogs",
	AdaptiveRouter = "AdaptiveRouter",
	AuditLogs = "AuditLogs",
	Customers = "Customers",
	BusinessUnits = "BusinessUnits",
	Teams = "Teams",
	RBAC = "RBAC",
	Governance = "Governance",
	RoutingRules = "RoutingRules",
	PromptRepository = "PromptRepository",
	PromptDeploymentStrategy = "PromptDeploymentStrategy",
	AccessProfiles = "AccessProfiles",
	Projects = "Projects",
	APIKeys = "APIKeys",
	Inference = "Inference",
	Metrics = "Metrics",
	FeatureFlags = "FeatureFlags",
	CircuitBreaker = "CircuitBreaker",
	Devices = "Devices",
	Inventory = "Inventory",
	EdgeConfig = "EdgeConfig",
	SkillsRepository = "SkillsRepository",
	Notifications = "Notifications",
	Warp = "Warp",
	WarpSession = "WarpSession",
	BackgroundJobs = "BackgroundJobs",
}

// RBAC Operation Names (must match backend definitions)
export enum RbacOperation {
	Read = "Read",
	View = "View",
	Create = "Create",
	Update = "Update",
	Delete = "Delete",
	Reveal = "Reveal",
	Download = "Download",
	CreateStandalone = "CreateStandalone",
}

interface RbacContextType {
	isAllowed: (resource: RbacResource, operation: RbacOperation) => boolean;
	permissions: Record<string, Record<string, boolean>>;
	isLoading: boolean;
	refetch: () => void;
}

const RbacContext = createContext<RbacContextType | null>(null);

// Dummy provider that allows all permissions
export function RbacProvider({ children }: { children: React.ReactNode }) {
	return (
		<RbacContext.Provider
			value={{
				isAllowed: () => true, // Always allow in OSS
				permissions: {},
				isLoading: false,
				refetch: () => {},
			}}
		>
			{children}
		</RbacContext.Provider>
	);
}

// Enterprise-only resources. Their surfaces are upsell placeholders in OSS
// ("Unlock … enterprise license" views), so OSS builds report no access: the
// sidebar hides their entries and their route layouts render NoPermissionView
// instead of the placeholder. Shared resources (Teams, Customers, VirtualKeys,
// Governance, Settings, …) keep returning true — their OSS pages are real.
const ENTERPRISE_ONLY_RESOURCES = new Set<RbacResource>([
	RbacResource.Users,
	RbacResource.BusinessUnits,
	RbacResource.UserProvisioning,
	RbacResource.AuditLogs,
	RbacResource.RBAC,
	RbacResource.Cluster,
	RbacResource.GuardrailsConfig,
	RbacResource.GuardrailsProviders,
	RbacResource.GuardrailRules,
	RbacResource.CircuitBreaker,
	RbacResource.AdaptiveRouter,
	RbacResource.AccessProfiles,
	RbacResource.Projects,
	RbacResource.APIKeys,
	RbacResource.Devices,
	RbacResource.Inventory,
	RbacResource.EdgeConfig,
	RbacResource.PromptDeploymentStrategy,
]);

// Hook: unrestricted in OSS, except for enterprise-only resources (see above).
export function useRbac(resource: RbacResource, _operation: RbacOperation): boolean {
	return !ENTERPRISE_ONLY_RESOURCES.has(resource);
}

// Hook to access full RBAC context
export function useRbacContext() {
	const context = useContext(RbacContext);
	if (!context) {
		// Return dummy values if used outside provider
		return {
			isAllowed: () => true,
			permissions: {},
			isLoading: false,
			refetch: () => {},
		};
	}
	return context;
}