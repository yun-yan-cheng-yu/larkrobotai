package feishutools

import "testing"

func TestParseLink(t *testing.T) {
	cases := []struct {
		in         string
		wantKind   string
		wantToken  string
		wantTable  string
		wantParsed bool
	}{
		{"https://xxx.feishu.cn/wiki/ABCdef123456?t=1", "wiki", "ABCdef123456", "", true},
		{"https://xxx.feishu.cn/docx/QWERzxcv0987", "docx", "QWERzxcv0987", "", true},
		{"https://xxx.feishu.cn/sheets/shtcnABC123", "sheets", "shtcnABC123", "", true},
		{"https://xxx.feishu.cn/base/APPtoken123?table=tblABCD1234&view=vew1", "base", "APPtoken123", "tblABCD1234", true},
		{"https://xxx.feishu.cn/file/xxx", "", "", "", false},
		{"QWERzxcv09871234", "", "QWERzxcv09871234", "", true}, // 裸 token
		{"", "", "", "", false},
		{"随便说说话", "", "", "", false},
	}
	for _, c := range cases {
		ref, ok := ParseLink(c.in)
		if ok != c.wantParsed {
			t.Fatalf("ParseLink(%q) parsed=%v, 期望 %v", c.in, ok, c.wantParsed)
		}
		if !ok {
			continue
		}
		if ref.Kind != c.wantKind || ref.Token != c.wantToken || ref.TableID != c.wantTable {
			t.Fatalf("ParseLink(%q) = %+v, 期望 kind=%s token=%s table=%s", c.in, ref, c.wantKind, c.wantToken, c.wantTable)
		}
	}
}
