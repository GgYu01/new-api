package codex

import "github.com/QuantumNous/new-api/setting/ratio_setting"

var baseModelList = []string{
	"gpt-5.6-sol",
	"gpt-5.6-terra",
	"gpt-5.6-luna",
	"gpt-5.5",
	"gpt-5.4",
	"gpt-5.4-mini",
	"gpt-5.3-codex-spark",
}

var ModelList = ratio_setting.WithCompactModelVariants(baseModelList)

const ChannelName = "codex"
