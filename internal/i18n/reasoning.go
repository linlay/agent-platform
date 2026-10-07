package i18n

import (
	"hash/fnv"
	"strings"
)

// Keep both languages in the same fixed order so the selected phrase is
// independent of the viewer's locale.
var reasoningLabels = [...]struct{ en, zhCN string }{
	{"Thinking", "思考"},
	{"Pondering", "琢磨"},
	{"Deliberating", "斟酌"},
	{"Cogitating", "思索"},
	{"Contemplating", "沉思"},
	{"Mulling", "推敲"},
	{"Musing", "遐想"},
	{"Reasoning", "推理"},
	{"Inferring", "推断"},
	{"Calculating", "演算"},
	{"Computing", "计算"},
	{"Deciphering", "解读"},
	{"Unraveling", "抽丝剥茧"},
	{"Synthesizing", "融会贯通"},
	{"Coalescing", "汇聚思路"},
	{"Crystallizing", "凝练"},
	{"Envisioning", "构想"},
	{"Imagining", "想象"},
	{"Ideating", "萌生灵感"},
	{"Sketching", "勾勒"},
	{"Composing", "构思"},
	{"Crafting", "精雕细琢"},
	{"Forging", "锻造思路"},
	{"Brewing", "酝酿"},
	{"Percolating", "萃取灵感"},
	{"Simmering", "慢炖思绪"},
	{"Incubating", "孵化"},
	{"Hatching", "孕育灵感"},
	{"Polishing", "打磨"},
	{"Tinkering", "拨弄思绪"},
}

// ReasoningLabelForID selects a display phrase using 32-bit FNV-1a over the
// trimmed ID's UTF-8 bytes, then resolves that phrase in the viewer's locale.
func ReasoningLabelForID(locale, reasoningID string) string {
	index := uint32(0)
	if reasoningID = strings.TrimSpace(reasoningID); reasoningID != "" {
		hasher := fnv.New32a()
		_, _ = hasher.Write([]byte(reasoningID))
		index = hasher.Sum32() % uint32(len(reasoningLabels))
	}
	label := reasoningLabels[index]
	if ResolveLocale(locale) == LocaleZhCN {
		return label.zhCN
	}
	return label.en
}
