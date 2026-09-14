package codex

import "github.com/QuantumNous/new-api/setting/ratio_setting"

var baseModelList = []string{
	"codexautoreview",
	"codex-auto-review",
	"gpt-5.6-sol",
	"gpt-5.6-terra",
	"gpt-5.6-luna",
	"gpt-6",
	"gpt-6-astra",
	"gpt-6-sol",
	"gpt-6-terra",
	"gpt-5.3-codex-spark",
}

var ModelList = ratio_setting.WithCompactModelVariants(baseModelList)

const ChannelName = "codex"
