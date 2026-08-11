package main

import (
	"errors"
	"fmt"
)

type opencodeCatalog struct{}

func (opencodeCatalog) RootPackage() opencodePackage {
	return opencodePackage{
		Name:      "opencode-ai",
		Integrity: "sha512-l4nUfoucuw8u5WYU9my9Yz7lYpBI649i/ppgL0BGTjp/HC3p2jN50i331YpcGbKfGTEv9VG6mxU1+QZyaR5hxA==",
	}
}

func (opencodeCatalog) PlatformPackage(goos, goarch string) (opencodePackage, error) {
	platform := goos + "/" + goarch
	packages := map[string]opencodePackage{
		"darwin/amd64": {
			Name: "opencode-darwin-x64-baseline", Integrity: "sha512-IxX00YOhWQ38f54ZR+g9bJTtRK7cUCKM7VzGaHbOgk8sfqAxNUJEhz1+BY/V0eODE76jh8lKM5Bjm/vqBno92Q==",
			ExecutableEntry: "package/bin/opencode",
		},
		"darwin/arm64": {
			Name: "opencode-darwin-arm64", Integrity: "sha512-/eEAcBOMOAv2c35s+1smy8+8VxGHOAbH8bIgcYdJJ8rJNMRMtSrdhsHFKsa/27oYbv1k/WHHU8XYddLvCoCXVw==",
			ExecutableEntry: "package/bin/opencode",
		},
		"linux/amd64": {
			Name: "opencode-linux-x64-baseline", Integrity: "sha512-Lvm4XLm918etLz85Yh8CCTcCalLUjx3TA8KVq3S4+EfTNBJ3QOmUyLjGQPhuC2kw+5NvkQVV/mnVdCawxnJ6ng==",
			ExecutableEntry: "package/bin/opencode",
		},
		"linux/arm64": {
			Name: "opencode-linux-arm64", Integrity: "sha512-0s32hDy72CBsT6sK7xsDUNKrACmylz5TIADHcYf8BXm7cHA/ry6fVNZ6r/RDtdQxRv6Hr47bynx+NJ8rm9SZAA==",
			ExecutableEntry: "package/bin/opencode",
		},
		"windows/amd64": {
			Name: "opencode-windows-x64-baseline", Integrity: "sha512-5ZnpdRq4KICElnb/OQ1PtufgmcxAYLILEJiu9rKJjAxTYjFEWMkpA6SRiU4LRbYzQn8LaHObDgxzt7bquA0OTw==",
			ExecutableEntry: "package/bin/opencode.exe",
		},
		"windows/arm64": {
			Name: "opencode-windows-arm64", Integrity: "sha512-FZrB40RBm5gvv3Uv+WOSRlEHQsqcJ04t7B3yp/L6SFYU6T2UZQqvLwDF/TPT1C0//a8uAbfqUV1h26sTmsi4ow==",
			ExecutableEntry: "package/bin/opencode.exe",
		},
	}
	result, exists := packages[platform]
	if !exists {
		return opencodePackage{}, fmt.Errorf("OpenCode evaluation does not support %s", platform)
	}
	if result.Name == "" || result.Integrity == "" || result.ExecutableEntry == "" {
		return opencodePackage{}, errors.New("OpenCode platform package contract is incomplete")
	}
	return result, nil
}

func (opencodeCatalog) Models() []opencodeModel {
	return []opencodeModel{
		{Label: "gpt-oss-20b", Route: "openai/gpt-oss-20b:free", ContextTokens: 131072},
		{Label: "gemma-4-31b", Route: "google/gemma-4-31b-it:free", ContextTokens: 262144},
		{Label: "laguna-s-2.1", Route: "poolside/laguna-s-2.1:free", ContextTokens: 262144},
	}
}
