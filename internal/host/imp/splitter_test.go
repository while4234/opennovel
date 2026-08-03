package imp

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestSplitText_Chinese(t *testing.T) {
	src := `第一章 初见
张三走进客栈，要了一壶酒。

李四从角落抬起头。

第二章 离别
天亮时张三起身告辞。

第三章：决战
雪夜，二人相对。`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 3 {
		t.Fatalf("want 3 chapters, got %d", len(got))
	}
	want := []struct{ title, headOf string }{
		{"初见", "张三走进客栈"},
		{"离别", "天亮时张三起身告辞"},
		{"决战", "雪夜"},
	}
	for i, w := range want {
		if got[i].Title != w.title {
			t.Errorf("ch%d title: got %q want %q", i+1, got[i].Title, w.title)
		}
		if !strings.HasPrefix(got[i].Content, w.headOf) {
			t.Errorf("ch%d content head: got %q want prefix %q", i+1, got[i].Content, w.headOf)
		}
	}
}

func TestSplitText_ChineseWithMarkdownPrefix(t *testing.T) {
	src := `# 第1章 起航
正文一。

## 第二回 风浪
正文二。`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 2 {
		t.Fatalf("want 2 chapters, got %d", len(got))
	}
	if got[0].Title != "起航" || got[1].Title != "风浪" {
		t.Errorf("titles wrong: %+v", got)
	}
}

func TestSplitText_English(t *testing.T) {
	src := `Chapter 1: The Beginning
Hero awoke at dawn.

Chapter II. Crossing
The river ran cold.

CHAPTER 3 Final
A blade fell.`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 3 {
		t.Fatalf("want 3 chapters, got %d", len(got))
	}
	if got[0].Title != "The Beginning" {
		t.Errorf("ch1 title: %q", got[0].Title)
	}
	if got[1].Title != "Crossing" {
		t.Errorf("ch2 title: %q", got[1].Title)
	}
	if got[2].Title != "Final" {
		t.Errorf("ch3 title: %q", got[2].Title)
	}
}

func TestSplitText_Volume(t *testing.T) {
	src := `第一卷 风起
卷一正文。

卷二 云涌
卷二正文。`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].Title != "风起" || got[1].Title != "云涌" {
		t.Errorf("volume titles wrong: %+v", got)
	}
}

func TestSplitText_VolumePrefixedChapterTitles(t *testing.T) {
	src := `卷一 白蛇妖仙 引子
引子正文。
二
引子第二节正文。

卷一 白蛇妖仙 第一章 医大女鬼
正文一。

卷一 白蛇妖仙 灵异档案 白蛇异冢真实原形
资料正文。`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	wantTitles := []string{"引子", "医大女鬼"}
	for i, want := range wantTitles {
		if got[i].Title != want {
			t.Errorf("chapter %d title: got %q want %q", i+1, got[i].Title, want)
		}
	}
	if !strings.Contains(got[0].Content, "二\n引子第二节正文") {
		t.Errorf("bare Chinese numerals should stay inside chapter body: %q", got[0].Content)
	}
}

func TestSplitText_EpisodeSectionChapterTitles(t *testing.T) {
	src := `第一集   奔向黎明 第一节  来自麻省理工的初中代课老师
正文一。

第一集   奔向黎明 第二节  哥尼斯堡七桥问题
正文二。

第九集 八部天龙 第一节
正文三。

第十四集 天下风云出我辈 第1节 PK赛
正文四。

第二十一集疯狂时代 第一节最后的晚餐
正文五。

第一节课是数学课，不能误切。
仍然是第五节正文。`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 5 {
		t.Fatalf("want 5, got %d: %+v", len(got), got)
	}
	wantTitles := []string{
		"来自麻省理工的初中代课老师",
		"哥尼斯堡七桥问题",
		"八部天龙",
		"PK赛",
		"最后的晚餐",
	}
	for i, want := range wantTitles {
		if got[i].Title != want {
			t.Fatalf("chapter %d title: got %q want %q", i+1, got[i].Title, want)
		}
	}
	if strings.Contains(got[4].Content, "正文四") || !strings.Contains(got[4].Content, "第一节课是数学课") {
		t.Fatalf("episode section chapter content split incorrectly: %+v", got[4])
	}
}

func TestSplitText_NonStoryHeadingsAreBoundariesOnly(t *testing.T) {
	src := `卷一 白蛇妖仙 尾声
尾声正文。

卷二 蛇指影魔预告
预告正文不应进入上一章。

卷一 白蛇妖仙 灵异档案 白蛇异冢真实原形
资料正文不应成为章节。

卷二 蛇指影魔 引子
引子正文。`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].Title != "尾声" || got[1].Title != "引子" {
		t.Fatalf("titles: %+v", got)
	}
	if strings.Contains(got[0].Content, "预告正文") || strings.Contains(got[1].Content, "资料正文") {
		t.Fatalf("non-story content leaked into chapters: %+v", got)
	}
}

func TestSplitText_LegitimateChapterTitleMayContainPreviewWord(t *testing.T) {
	src := `第一章 死亡预告
正文一。

第二章 真相
正文二。`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].Title != "死亡预告" {
		t.Fatalf("title: got %q want %q", got[0].Title, "死亡预告")
	}
}

func TestSplitText_BareChineseChapterNumber(t *testing.T) {
	src := `第十章 纨绔子弟
正文十。

十一章 难以接受
正文十一。

廿一章　旗袍研究
正文二十一。`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
	wantTitles := []string{"纨绔子弟", "难以接受", "旗袍研究"}
	for i, want := range wantTitles {
		if got[i].Title != want {
			t.Fatalf("chapter %d title: got %q want %q", i+1, got[i].Title, want)
		}
	}
}

func TestSplitText_BareChineseSectionNumber(t *testing.T) {
	src := `第十节 拜师
正文十。

十一节 下药的猫腻
正文十一。

二十节 执子之手
正文二十。`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
	wantTitles := []string{"拜师", "下药的猫腻", "执子之手"}
	for i, want := range wantTitles {
		if got[i].Title != want {
			t.Fatalf("chapter %d title: got %q want %q", i+1, got[i].Title, want)
		}
	}
}

func TestSplitText_ArabicChapterNumberWithCompactTitle(t *testing.T) {
	src := `正文 第01章 意外发现
正文一。

正文 第02章 有问题？
正文二。

正文 第03章满足的回味
正文三。

第04章女上司的另一面
正文四。`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 4 {
		t.Fatalf("want 4, got %d", len(got))
	}
	wantTitles := []string{"意外发现", "有问题？", "满足的回味", "女上司的另一面"}
	for i, want := range wantTitles {
		if got[i].Title != want {
			t.Fatalf("chapter %d title: got %q want %q", i+1, got[i].Title, want)
		}
	}
}

func TestSplitText_ParenthesizedChineseChapterNumbers(t *testing.T) {
	src := `（一）
正文一。

(二)
正文二。
2004-6-816:01#1

free990614该用户已被删除

精华积分N/A帖子阅读权限注册N/A（三）

正文三。`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
	assertChapterTitlesAt(t, got, 1, []string{"（一）", "(二)", "（三）"})
	if strings.Contains(got[1].Content, "2004-6-8") ||
		strings.Contains(got[1].Content, "该用户已被删除") ||
		strings.Contains(got[1].Content, "精华积分") {
		t.Fatalf("source-site metadata leaked into chapter body: %q", got[1].Content)
	}
	if !strings.HasPrefix(got[2].Content, "正文三") {
		t.Fatalf("chapter 3 content: %q", got[2].Content)
	}
}

func TestSplitText_ParenthesizedChineseMarkersDoNotOverrideStandardChapters(t *testing.T) {
	src := `第一章 开始
（一）
小节正文。

第二章 继续
正文二。`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	assertChapterTitlesAt(t, got, 1, []string{"开始", "继续"})
	if !strings.Contains(got[0].Content, "（一）") || !strings.Contains(got[0].Content, "小节正文") {
		t.Fatalf("parenthesized section should stay in chapter body: %q", got[0].Content)
	}
}

func TestSplitText_BracketedWorkTitlePrefix(t *testing.T) {
	src := `【女神攻略】第二章 目睹
正文二。

【女神攻略】第九章银屏女神
正文九。

【女神攻略-同人续】第十五章 一日千里
正文十五。`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
	wantTitles := []string{"目睹", "银屏女神", "一日千里"}
	for i, want := range wantTitles {
		if got[i].Title != want {
			t.Fatalf("chapter %d title: got %q want %q", i+1, got[i].Title, want)
		}
	}
}

func TestSplitText_RepeatedSourceTitleWithMetadataIsCollapsed(t *testing.T) {
	src := `【女神攻略】第十三章 二龙戏珠（上）
作者：ntr2017-03-02字数：13778
第十三章 二龙戏珠（上）
正文十三。

【女神攻略-同人续】第十五章 一日千里
2023年9月18日
【第十五章·一日千里】
正文十五。`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	assertChapterTitlesAt(t, got, 1, []string{"二龙戏珠（上）", "一日千里"})
	if strings.Contains(got[0].Content, "作者：") || strings.Contains(got[1].Content, "2023年9月18日") {
		t.Fatalf("metadata-only duplicate heading preface leaked into content: %+v", got)
	}
}

func TestSplitText_SourceSiteFreeReadingFragmentsAreMerged(t *testing.T) {
	src := `第953章 迦乐世太小了
正文一。
为您提供大神听日的《术师手册》最快更新，！
    第953章 迦乐世太小了免费阅读：，！
    『』 ，最快更新最新章节！
正文二。
为您提供大神听日的《术师手册》最快更新，！
    第953章 迦乐世太小了免费阅读：，！
    『』
------------
夜之城请假条
昨晚卡文，今天请假。
------------
第954章 等我
正文三。`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 2 {
		t.Fatalf("want 2 chapters, got %d: %+v", len(got), got)
	}
	assertChapterTitlesAt(t, got, 1, []string{"迦乐世太小了", "等我"})
	if !strings.Contains(got[0].Content, "正文一。") || !strings.Contains(got[0].Content, "正文二。") {
		t.Fatalf("free-reading fragments should merge into the real chapter: %q", got[0].Content)
	}
	for _, forbidden := range []string{"免费阅读", "最快更新", "『』", "夜之城请假条", "------------"} {
		if strings.Contains(got[0].Content, forbidden) {
			t.Fatalf("source-site noise %q leaked into chapter: %q", forbidden, got[0].Content)
		}
	}
}

func TestSplitText_InlineTrailingChapterHeading(t *testing.T) {
	src := `第十二章 老谋深算
上一章正文。
一句话结束。”第十三章 最后清算
下一章正文。`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].Title != "老谋深算" || got[1].Title != "最后清算" {
		t.Fatalf("titles: %+v", got)
	}
	if !strings.Contains(got[0].Content, "一句话结束。") || strings.Contains(got[1].Content, "一句话结束") {
		t.Fatalf("inline split placed content incorrectly: %+v", got)
	}
}

func TestSplitText_PureVolumeHeadingBeforeChapterIsSkipped(t *testing.T) {
	src := `卷十四  藏镜罗刹
引子一
引子一正文。

引子二
引子二正文。

第一章 幼女自尽
第一章正文。`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
	wantTitles := []string{"引子一", "引子二", "幼女自尽"}
	for i, want := range wantTitles {
		if got[i].Title != want {
			t.Errorf("chapter %d title: got %q want %q", i+1, got[i].Title, want)
		}
	}
	if strings.Contains(got[0].Title, "藏镜罗刹") {
		t.Errorf("pure volume heading must not become a source chapter: %+v", got)
	}
}

func TestSplitText_SpecialUnits(t *testing.T) {
	src := `楔子
古老的传说。

第一章 启程
踏上旅途。

尾声：归乡
多年以后。

番外
番外正文。`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 4 {
		t.Fatalf("want 4, got %d", len(got))
	}
	wantTitles := []string{"楔子", "启程", "归乡", "番外"}
	for i, w := range wantTitles {
		if got[i].Title != w {
			t.Errorf("unit %d title: got %q want %q", i+1, got[i].Title, w)
		}
	}
}

func TestSplitText_SpecialUnitNumericSubtitleFallsBackToKeyword(t *testing.T) {
	src := `尾声 　　一
尾声正文。`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
	if got[0].Title != "尾声" {
		t.Fatalf("title: got %q want %q", got[0].Title, "尾声")
	}
}

func TestSplitText_EnglishPrologueEpilogue(t *testing.T) {
	src := `Prologue
Before it all began.

Chapter 1 The Start
Here we go.

Epilogue: After
Years later.`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
	if got[2].Title != "After" {
		t.Errorf("epilogue title: %q", got[2].Title)
	}
}

func TestSplitText_NoTitle_FallsBack(t *testing.T) {
	src := `第一章
没有空格的标题，正文紧跟。

第二章
第二段正文。`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].Title != "第1章" || got[1].Title != "第2章" {
		t.Errorf("fallback titles wrong: %+v", got)
	}
}

func TestSplitText_NoMatches(t *testing.T) {
	src := `这是一段没有任何章节标题的文本。
全部按一段处理。`
	got := splitText(src, defaultChapterRegex)
	if len(got) != 0 {
		t.Errorf("want empty, got %d", len(got))
	}
}

func TestSplitText_EmptyChapterSkipped(t *testing.T) {
	src := `第一章 标题
正文。

第二章 空章

第三章 末章
末章正文。`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 2 {
		t.Fatalf("want 2 (skip empty), got %d", len(got))
	}
	if got[0].Title != "标题" || got[1].Title != "末章" {
		t.Errorf("titles after skip: %+v", got)
	}
}

func TestSplitText_TrailingLicenseStripped(t *testing.T) {
	src := `Chapter 1 Start
First chapter body.

Project Gutenberg eBook
LICENSE TEXT HERE
END OF EBOOK`

	got := splitText(src, defaultChapterRegex)
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
	if strings.Contains(got[0].Content, "Project Gutenberg") {
		t.Errorf("trailer not stripped: %q", got[0].Content)
	}
	if !strings.HasPrefix(got[0].Content, "First chapter body.") {
		t.Errorf("body head wrong: %q", got[0].Content)
	}
}

func TestSplitText_FullWidthSpace(t *testing.T) {
	src := "第一章　风起\n北风卷地。\n\n第2章　\n云动。\n"
	got := splitText(src, defaultChapterRegex)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].Title != "风起" {
		t.Errorf("ch1 title: %q", got[0].Title)
	}
	if got[1].Title != "第2章" { // 仅尾随全角空格 → 回退占位标题
		t.Errorf("ch2 title: %q", got[1].Title)
	}
}

func TestSplitText_BodyPrefix(t *testing.T) {
	src := "正文 第一章 风起\n北风。\n\n正文　第二章　云涌\n乌云。\n"
	got := splitText(src, defaultChapterRegex)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].Title != "风起" || got[1].Title != "云涌" {
		t.Errorf("titles: %+v", got)
	}
}

func TestSplitFile_ReadsAndSplits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "novel.txt")
	src := "第一章 起\n正文 A\n\n第二章 终\n正文 B\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := SplitFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
}

func TestSplitFile_EmptyError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(path, []byte("   \n  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := SplitFile(path)
	if err == nil {
		t.Fatal("want error for empty file")
	}
}

func TestSplitFile_GBKEncoded(t *testing.T) {
	src := "第一章 起\n正文 A\n\n第二章 终\n正文 B\n"
	data, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "gbk.txt")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := SplitFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].Title != "起" || got[1].Title != "终" {
		t.Errorf("titles: %+v", got)
	}
}

func TestSplitFile_UTF8BOM(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bom.txt")
	src := "\uFEFF第一章 起\n正文 A\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := SplitFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "起" {
		t.Fatalf("BOM file: %+v", got)
	}
}

func TestSplitFile_UTF16BOM(t *testing.T) {
	src := "第一章 起\n正文 A\n\n第二章 终\n正文 B\n"
	cases := []struct {
		name string
		data []byte
	}{
		{name: "le", data: encodeUTF16WithBOM(src, binary.LittleEndian, []byte{0xFF, 0xFE})},
		{name: "be", data: encodeUTF16WithBOM(src, binary.BigEndian, []byte{0xFE, 0xFF})},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "utf16.txt")
			if err := os.WriteFile(path, tc.data, 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := SplitFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 2 {
				t.Fatalf("want 2, got %d", len(got))
			}
			if got[0].Title != "起" || got[1].Title != "终" {
				t.Errorf("titles: %+v", got)
			}
		})
	}
}

func TestSplitFile_InvalidUTF8BytesAreCleaned(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "invalid.txt")
	data := []byte("第一章 起\n正文")
	data = append(data, 0xFF)
	data = append(data, []byte("继续。\n")...)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := SplitFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
	if !utf8.ValidString(got[0].Title) || !utf8.ValidString(got[0].Content) {
		t.Fatal("chapter text must be valid UTF-8")
	}
}

func TestSplitFile_ExternalGazFixture(t *testing.T) {
	path := os.Getenv("AINOVEL_GAZ_FIXTURE")
	if path == "" {
		t.Skip("set AINOVEL_GAZ_FIXTURE to run the external gaz.txt splitter acceptance test")
	}
	got, err := SplitFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 237 {
		t.Fatalf("gaz fixture chapters: got %d want 237", len(got))
	}
	assertChapterTitlesAt(t, got, 1, []string{
		"引子", "医大女鬼", "一零六室", "又闻失心", "此人已死",
		"白蛇异冢", "双鬼夜袭", "樟树秘道", "谁是女鬼", "幕后黑手",
		"弃卒保帅", "夜探水道", "首脑落网", "尾声", "引子",
		"夺命鬼影", "死亡威胁", "影楼老板", "十三年前", "忠犬弑主",
	})
	assertContainsTitleSequence(t, got, []string{"蛇指影魔 纤凌前传 彼得洛希卡", "引子", "九天化尸"})
	assertContainsTitleSequence(t, got, []string{"伟哥与Sailor moon", "引子一", "引子二"})
	assertContainsTitleSequence(t, got, []string{"引子一", "引子二", "幼_女自尽"})
	assertContainsTitleSequence(t, got, []string{
		"难以接受", "洞内对决", "关键提示", "神秘毒素", "密室凶案",
		"楼顶之秘", "秘密信息", "通话录音", "鬼爪神功",
	})
	assertChapterTitlesAt(t, got, 218, []string{
		"异香飘落", "轮回圣坛", "深入鬼穴", "尾声",
		"引子", "人心难测", "母子连心", "邪教起源",
		"五行引魂", "隐瞒真相", "拦途截劫", "重归于好", "覆雨翻云",
		"密码解读", "人剑交易", "单刀赴会", "老谋深算", "最后清算",
		"他的秘密", "尾声",
	})
	for i, ch := range got {
		if ch.Title == "藏镜罗刹" || strings.Contains(ch.Title, "灵异档案") || strings.Contains(ch.Title, "预告") {
			t.Fatalf("non-story heading became chapter %d: %q", i+1, ch.Title)
		}
	}
}

func TestSplitFile_ExternalNsglFixture(t *testing.T) {
	path := os.Getenv("AINOVEL_NSGL_FIXTURE")
	if path == "" {
		t.Skip("set AINOVEL_NSGL_FIXTURE to run the external nsgl.txt splitter acceptance test")
	}
	got, err := SplitFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 17 {
		t.Fatalf("nsgl fixture chapters: got %d want 17", len(got))
	}
	assertChapterTitlesAt(t, got, 1, []string{
		"顾茹", "目睹", "浑水摸鱼", "黄雀在后", "谋划",
		"手机！手机！", "漫长的一天", "车震？", "银屏女神", "璐璐的转变",
		"生病与睡觉", "糊涂人明白事", "二龙戏珠（上）", "二龙戏珠（下）第(1/8)页",
		"一日千里", "真假救美", "医院迷情",
	})
}

func TestSplitFile_ExternalMzdnhFixture(t *testing.T) {
	path := os.Getenv("AINOVEL_MZDNH_FIXTURE")
	if path == "" {
		t.Skip("set AINOVEL_MZDNH_FIXTURE to run the external mzdnh.txt splitter acceptance test")
	}
	got, err := SplitFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 17 {
		t.Fatalf("mzdnh fixture chapters: got %d want 17", len(got))
	}
	assertChapterTitlesAt(t, got, 1, []string{
		"（一）", "（二）", "（三）", "（四）", "（五）", "（六）", "（七）", "（八）", "（九）",
		"（十）", "（十一）", "（十二）", "（十三）", "（十四）", "（十五）", "（十六）", "（十七）",
	})
	for i, ch := range got {
		if strings.Contains(ch.Content, "精华积分") || strings.Contains(ch.Content, "该用户已被删除") {
			t.Fatalf("source-site metadata leaked into chapter %d: %q", i+1, ch.Content)
		}
	}
}

func assertChapterTitlesAt(t *testing.T, got []Chapter, start int, want []string) {
	t.Helper()
	for i, title := range want {
		idx := start - 1 + i
		if idx >= len(got) {
			t.Fatalf("missing chapter %d, want title %q", idx+1, title)
		}
		if got[idx].Title != title {
			t.Fatalf("chapter %d title: got %q want %q", idx+1, got[idx].Title, title)
		}
	}
}

func assertContainsTitleSequence(t *testing.T, got []Chapter, want []string) {
	t.Helper()
	for start := 0; start+len(want) <= len(got); start++ {
		matched := true
		for i, title := range want {
			if got[start+i].Title != title {
				matched = false
				break
			}
		}
		if matched {
			return
		}
	}
	t.Fatalf("missing title sequence: %v", want)
}

func TestSplitText_SectionAndAct(t *testing.T) {
	src := `第一节 开端
正文一。

第二幕 高潮
正文二。`
	got := splitText(src, defaultChapterRegex)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].Title != "开端" || got[1].Title != "高潮" {
		t.Errorf("titles: %+v", got)
	}
}

func TestSplitText_UppercaseNumbers(t *testing.T) {
	src := `第壹章 起
正文一。

第贰拾章 终
正文二。`
	got := splitText(src, defaultChapterRegex)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].Title != "起" || got[1].Title != "终" {
		t.Errorf("titles: %+v", got)
	}
}

func TestSplitText_BracketWrapped(t *testing.T) {
	src := `【第一章 风起】
正文一。

〖第二章 云涌〗
正文二。

【第十五章·一日千里】
正文三。

【楔子】
楔子正文。`
	got := splitText(src, defaultChapterRegex)
	if len(got) != 4 {
		t.Fatalf("want 4, got %d", len(got))
	}
	if got[0].Title != "风起" || got[1].Title != "云涌" {
		t.Errorf("titles: %+v", got)
	}
	if got[2].Title != "一日千里" {
		t.Errorf("middle-dot title: %q", got[2].Title)
	}
	if got[3].Title != "楔子" {
		t.Errorf("bracket spkw title: %q", got[3].Title)
	}
}

func encodeUTF16WithBOM(src string, order binary.ByteOrder, bom []byte) []byte {
	units := utf16.Encode([]rune(src))
	data := append([]byte{}, bom...)
	var buf [2]byte
	for _, unit := range units {
		order.PutUint16(buf[:], unit)
		data = append(data, buf[:]...)
	}
	return data
}
