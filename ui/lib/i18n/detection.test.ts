import { describe, expect, it } from "vitest";
import { detectLanguage } from "./detection";

describe("detectLanguage", () => {
	it("stored preference wins over browser languages", () => {
		expect(detectLanguage("zh-CN", ["en-US"])).toBe("zh-CN");
		expect(detectLanguage("en", ["zh-CN"])).toBe("en");
	});

	it("detects zh from any zh-* browser language", () => {
		expect(detectLanguage(null, ["zh-CN", "en"])).toBe("zh-CN");
		expect(detectLanguage(null, ["zh-TW"])).toBe("zh-CN");
		expect(detectLanguage(undefined, ["en-US", "zh"])).toBe("en");
	});

	it("falls back to en when nothing matches", () => {
		expect(detectLanguage(null, ["fr-FR", "ja-JP"])).toBe("en");
		expect(detectLanguage(null, [])).toBe("en");
		expect(detectLanguage(undefined, undefined)).toBe("en");
	});

	it("treats invalid stored values as unset", () => {
		expect(detectLanguage("fr", ["en-US"])).toBe("en");
		expect(detectLanguage("zh", ["en-US"])).toBe("en");
		expect(detectLanguage("", [])).toBe("en");
	});
});