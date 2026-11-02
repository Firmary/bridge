package codex

import "github.com/Firmary/bridge/internal/driver"

// BuiltInPin is the Codex version this build runs until the signed update manifest (slice S5) supplies pins: the
// latest stable release, rust-v0.166.0 (https://github.com/openai/codex/releases/tag/rust-v0.166.0).
// ArchiveSHA256 is GitHub's published digest of each codex-<target>.tar.gz release asset; SHA256 is the executable
// inside it (for Windows, equal to the release's bare codex-<target>.exe asset). Both were checked on 2026-11-02.
var BuiltInPin = driver.Pin{
	Version: "0.166.0",
	ArchiveSHA256: map[string]string{
		"darwin-arm64": "c48824462fc57681eafb0925d97b11b589010475613904f570b42fea53e240d5",
		"darwin-x64":   "db0c38713c36199b12e7c8d1629386c35a4ce8f99e1c9ce72f1725a74df67b57",
		"linux-arm64":  "94eb707605ec2646b2e05b7edf7c4e7b3ccf02b16237c34ae9beb8cfb7f810a9",
		"linux-x64":    "a0bf572d2f93f056c6712a6da348e96078dc84f3885fd7be67807ef389cb3978",
		"win32-arm64":  "23cfbde45609292c325029a376340df30191298d7b6c5b26f607f481573ac768",
		"win32-x64":    "dafeb48b5a853b2898453a6b5bdae0fa0646bd5d5b73c5dfe74c97d0af4a4a0d",
	},
	SHA256: map[string]string{
		"darwin-arm64": "39acf979729dd290f9a24ee38b2914e036c91b65952872e28ae2a6b88a7db7c1",
		"darwin-x64":   "1361e18268866b7f9f5e8703fabcea0a2a990a439eedcce03940abb4ba9eaaeb",
		"linux-arm64":  "f2e6c980c937e734644bed1b4a9a018d3e63a0ccbe71aafc85c86a8c6e4b462a",
		"linux-x64":    "b8a11fedf24b2dfb59a70c9e52b030f9a508dcd49cd2dd0d41956f4c785156b8",
		"win32-arm64":  "d92bb6a3259980a742049882f396eb09eb26734589accc7f2c5173b7ab926138",
		"win32-x64":    "9d563ee4e0441ff9d59d612013e0460ddf8e5709735c2d9f6b98b7ef8ca4eb42",
	},
}
