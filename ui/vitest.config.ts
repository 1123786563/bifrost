import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import path from "path";

export default defineConfig({
	plugins: [react()],
	resolve: {
		alias: {
			"@": path.resolve(__dirname, "."),
			// Mirror the OSS branch of vite.config.mts: tests run against the
			// enterprise fallbacks so modules importing @enterprise/* resolve.
			"@enterprise": path.resolve(__dirname, "app", "_fallbacks", "enterprise"),
			"@schemas": path.resolve(__dirname, "app", "_fallbacks", "enterprise", "lib", "schemas"),
		},
	},
	define: {
		"process.env.BIFROST_IS_ENTERPRISE": JSON.stringify("false"),
	},
	test: {
		globals: true,
	},
});