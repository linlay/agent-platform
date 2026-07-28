package chat

import "testing"

func TestPreviewLastRunContentStripsThinkingTags(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "thinking block before answer",
			in:   "<thinking>内部推理</thinking>两个docx解压后目录递归 diff",
			want: "两个docx解压后目录递归 diff",
		},
		{
			name: "think block",
			in:   "<think>hidden</think>visible answer",
			want: "visible answer",
		},
		{
			name: "unclosed thinking tag",
			in:   "<thinking>只有开标签没有关",
			want: "",
		},
		{
			name: "plain text unchanged",
			in:   "正常预览文案",
			want: "正常预览文案",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := PreviewLastRunContent(tc.in); got != tc.want {
				t.Fatalf("PreviewLastRunContent(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
