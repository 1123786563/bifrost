import tailwindcss from "@tailwindcss/vite";
import { tanstackRouter } from "@tanstack/router-plugin/vite";
import react from "@vitejs/plugin-react";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vite";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const isEnterpriseBuild = fs.existsSync(path.join(__dirname, "app", "enterprise"));

export default defineConfig({
	plugins: [
		tanstackRouter({
			target: "react",
			routesDirectory: "./app",
			generatedRouteTree: "./app/routeTree.gen.ts",
			// All routes live in layout.tsx files. page.tsx files are pure view
			// components imported by their sibling layout.tsx (Next-style mental
			// model preserved for content, but routing config lives in one place).
			routeToken: "layout",
			// Treat ONLY layout.tsx / __root.tsx as routes; everything else under app/
			// (page.tsx, views, components, helpers) is ignored.
			// Directory entries have no extension and are not matched, so recursion still works.
			routeFileIgnorePattern: "^(?!layout\\.tsx$|__root\\.tsx$).+\\.(tsx|ts|jsx|js)$",
			autoCodeSplitting: true,
		}),
		react(),
		tailwindcss(),
	],
	resolve: {
		// Enterprise UI source is symlinked into ./app/enterprise; preserving symlinks
		// keeps module resolution rooted here so deps like zod / @phosphor-icons/react
		// resolve against this ui/node_modules rather than the symlink target's tree.
		preserveSymlinks: true,
		alias: {
			"@": path.resolve(__dirname),
			"@enterprise": isEnterpriseBuild
				? path.resolve(__dirname, "app", "enterprise")
				: path.resolve(__dirname, "app", "_fallbacks", "enterprise"),
			"@schemas": isEnterpriseBuild
				? path.resolve(__dirname, "app", "enterprise", "lib", "schemas")
				: path.resolve(__dirname, "app", "_fallbacks", "enterprise", "lib", "schemas"),
		},
	},
	define: {
		"process.env.NODE_ENV": JSON.stringify(process.env.NODE_ENV ?? "production"),
		"process.env.BIFROST_IS_ENTERPRISE": JSON.stringify(isEnterpriseBuild ? "true" : "false"),
		"process.env.BIFROST_DISABLE_PROFILER": JSON.stringify(process.env.BIFROST_DISABLE_PROFILER ?? ""),
		"process.env.BIFROST_ENTERPRISE_TRIAL_EXPIRY": JSON.stringify(process.env.ENTERPRISE_TRIAL_EXPIRY ?? ""),
		"process.env.BIFROST_PORT": JSON.stringify(process.env.BIFROST_PORT ?? ""),
	},
	server: {
		// Pin the dev server to explicit IPv4 loopback. With the default host
		// ("localhost"), Node ≥17 binds only ::1 on hosts where localhost resolves
		// IPv6-first, while the Go UI dev proxy (handlers/ui.go, uiDevServerAddr)
		// dials 127.0.0.1 — the proxy then fails and silently falls back to the
		// stale embedded UI. Both sides must stay pinned to the same explicit IP.
		host: "127.0.0.1",
		port: 3000,
		// The dashboard is usually opened through the Go gateway (e.g.
		// localhost:8080), whose dev proxy cannot carry WebSocket upgrades. Point
		// the HMR client straight at this dev server so live reload keeps working
		// behind the gateway and no upgrade ever transits the proxy.
		hmr: { host: "127.0.0.1", clientPort: 3000 },
		proxy: {
			"/api": {
				target: `http://127.0.0.1:${process.env.BIFROST_PORT || "8080"}`,
				changeOrigin: true,
			},
		},
	},
	build: {
		outDir: "out",
		emptyOutDir: true,
	},
});