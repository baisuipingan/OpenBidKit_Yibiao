package duplicate

import (
	"fmt"
	"testing"
)

func TestHeadingDetect(t *testing.T) {
	cases := []string{
		"## 第一章 资格证明",
		"第一章 资格证明",
		"一、资格要求",
		"1.1 项目背景",
		"（一）项目概况",
		"8000人次/日的门诊量",
		"投标人须提供营业执照副本复印件并加盖公章。",
	}
	for _, c := range cases {
		fmt.Printf("%-40s → %v\n", c, isHeadingLike(c))
	}
}
