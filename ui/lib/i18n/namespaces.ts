/** Every locale namespace. A batch translation task adds files per namespace; the
 * lazy glob loader and parity test pick new files up automatically. */
export const NAMESPACES = [
	"common",
	"workspaces",
	"setup",
	"auth",
	"dashboard",
	"providers",
	"governance",
	"logs",
	"playground",
	"mcp",
	"models",
	"plugins",
	"settings",
	"analytics",
	"errors",
] as const;
export type Namespace = (typeof NAMESPACES)[number];