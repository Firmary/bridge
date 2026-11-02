package claudecode

import "github.com/Firmary/firmary-helper/internal/driver"

// BuiltInPin is the Claude Code version this build runs until the signed update manifest (slice S5) supplies pins.
// Checksums are copied from Anthropic's signed manifest for 2.1.304
// (https://downloads.claude.ai/claude-code-releases/2.1.304/manifest.json); Ensure checks them against that
// manifest's signature again on every install.
var BuiltInPin = driver.Pin{
	Version: "2.1.304",
	SHA256: map[string]string{
		"darwin-arm64":     "b9ac5374839f14abf3f1f45610637c332b5daee5ce0d1634726cef87aab78e6a",
		"darwin-x64":       "f2ae409b46b1bdc1dec7afa12c6a094ff64e7d4ca272ca09f69dd0080c846703",
		"linux-arm64":      "c487117526509ab6d2ee6013e14f181faf68d6d6afe6bb0d4b2b4a9feca5ecb3",
		"linux-x64":        "ea0f135784631619a7a409d07322ecec964f9cc4fd377508ed1ba3f141f27912",
		"linux-arm64-musl": "d15758462f08940f6d2736cd422e291258b9bcaf09c013b63dcfca6cc69a5316",
		"linux-x64-musl":   "e11074c51f730bd91d0cd9568471d828d6552796b5dbcde10e7aaca19f1650c8",
		"win32-x64":        "019e5174d5d954585edb1a946fe280e44a973050b4897034adf0e16306f85fdd",
		"win32-arm64":      "ac37b04c19f09bea6638d1b3ca40131566266fc7b599a405efd76cd04df9e9a7",
	},
}
