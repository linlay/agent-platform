package stream

import (
	"hash/fnv"
	"strings"
)

var reasoningLabels = []string{
	"正在思考",
	"让我想一想",
	"等我捋一捋",
	"Thinking",
	"正在琢磨琢磨",
	"我要推敲一下",
	"正在反复斟酌",
	"Deliberating",
	"Pondering",
	"Computing",
	"Calculating",
	"Reasoning",
	"Inferring",
	"正在梳理中",
	"检查细节中",
	"正在归拢思路",
	"正在追线索",
	"顺藤摸瓜中",
	"斟酌中",
	"容我斟酌一下",
	"我再盘一盘",
	"还在酝酿",
	"还在沉淀中",
	"正在逐层推理",
	"提炼关键信息中",
	"想法还在成形",
	"抽丝剥茧中",
	"深思熟虑中",
	"正在由表及里",
	"正在融会贯通",
	"正在举一反三",
	"正在寻根究底",
	"审时度势中",
	"正在触类旁通",
	"穷源竟委中",
}

func ReasoningLabelForID(reasoningID string) string {
	reasoningID = strings.TrimSpace(reasoningID)
	if len(reasoningLabels) == 0 {
		return ""
	}
	if reasoningID == "" {
		return reasoningLabels[0]
	}
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(reasoningID))
	return reasoningLabels[hasher.Sum32()%uint32(len(reasoningLabels))]
}

