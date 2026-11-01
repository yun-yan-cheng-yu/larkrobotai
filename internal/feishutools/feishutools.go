// Package feishutools 提供原生的飞书云文档/知识库/表格/多维表格读取工具,
// 直接调用飞书 Open API(走应用的 tenant_access_token),不依赖任何外部进程。
// 注意:能读什么取决于应用被授予的 scope 与文档对应用的可见性(文档加协作者/知识库加机器人成员)。
package feishutools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkbitable "github.com/larksuite/oapi-sdk-go/v3/service/bitable/v1"
	larkdocx "github.com/larksuite/oapi-sdk-go/v3/service/docx/v1"
	larksheets "github.com/larksuite/oapi-sdk-go/v3/service/sheets/v3"
	larkwiki "github.com/larksuite/oapi-sdk-go/v3/service/wiki/v2"

	"larkrobotai/internal/llm"
)

const maxOut = 8000 // 工具输出截断长度(字符)

type Tools struct {
	api   *lark.Client
	names map[string]bool
}

func New(api *lark.Client) *Tools {
	return &Tools{api: api, names: map[string]bool{
		"feishu_read_doc":        true,
		"feishu_get_wiki_node":   true,
		"feishu_sheet_meta":      true,
		"feishu_sheet_find":      true,
		"feishu_bitable_tables":  true,
		"feishu_bitable_fields":  true,
		"feishu_bitable_records": true,
	}}
}

func (t *Tools) Owns(name string) bool { return t.names[name] }

// FeishuRef 从链接中解析出的资源定位
type FeishuRef struct {
	Kind    string // docx | wiki | sheets | base
	Token   string
	TableID string // base 链接 ?table=tblXXX 携带
}

var (
	refRe   = regexp.MustCompile(`/(docx|wiki|sheets|base)/([A-Za-z0-9]+)`)
	tableRe = regexp.MustCompile(`[?&]table=(tbl[A-Za-z0-9]+)`)
	bareRe  = regexp.MustCompile(`^[A-Za-z0-9]{10,}$`)
)

// ParseLink 解析飞书分享链接;裸 token 也能通过,但 Kind 为空需调用方指定
func ParseLink(raw string) (FeishuRef, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return FeishuRef{}, false
	}
	if m := refRe.FindStringSubmatch(raw); m != nil {
		ref := FeishuRef{Kind: m[1], Token: m[2]}
		if tm := tableRe.FindStringSubmatch(raw); tm != nil {
			ref.TableID = tm[1]
		}
		return ref, true
	}
	if bareRe.MatchString(raw) {
		return FeishuRef{Token: raw}, true
	}
	return FeishuRef{}, false
}

func (t *Tools) Defs() []llm.Tool {
	return []llm.Tool{
		{Type: "function", Function: llm.FunctionDef{
			Name:        "feishu_read_doc",
			Description: "读取飞书云文档的纯文本全文。ref 支持文档链接(https://xxx.feishu.cn/docx/... 或 /wiki/...)或 document_id。知识库链接会自动解析到真实文档。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"ref": map[string]any{"type": "string", "description": "文档链接或 document_id"},
				},
				"required": []string{"ref"},
			},
		}},
		{Type: "function", Function: llm.FunctionDef{
			Name:        "feishu_get_wiki_node",
			Description: "查询知识库节点信息(标题、类型、真实文档 token)。ref 支持 wiki 链接或 node token。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"ref": map[string]any{"type": "string", "description": "wiki 链接或 node token"},
				},
				"required": []string{"ref"},
			},
		}},
		{Type: "function", Function: llm.FunctionDef{
			Name:        "feishu_sheet_meta",
			Description: "获取飞书电子表格的标题与全部工作表(sheetId/标题/位置),供后续查找用。ref 支持 sheets 链接或 token。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"ref": map[string]any{"type": "string", "description": "电子表格链接或 token"},
				},
				"required": []string{"ref"},
			},
		}},
		{Type: "function", Function: llm.FunctionDef{
			Name:        "feishu_sheet_find",
			Description: "在飞书电子表格的某个工作表里查找包含关键字的单元格,返回单元格位置列表。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"ref":             map[string]any{"type": "string", "description": "电子表格链接或 token"},
					"sheet_id":        map[string]any{"type": "string", "description": "工作表 ID,先用 feishu_sheet_meta 获取"},
					"find":            map[string]any{"type": "string", "description": "要查找的关键字"},
					"match_case":      map[string]any{"type": "boolean", "description": "是否区分大小写,默认 false"},
					"match_entire":    map[string]any{"type": "boolean", "description": "是否整格完全匹配,默认 false"},
					"search_by_regex": map[string]any{"type": "boolean", "description": "是否按正则查找,默认 false"},
					"range":           map[string]any{"type": "string", "description": "可选,查找范围如 sheetId!A1:B100,默认整表"},
				},
				"required": []string{"ref", "sheet_id", "find"},
			},
		}},
		{Type: "function", Function: llm.FunctionDef{
			Name:        "feishu_bitable_tables",
			Description: "列出飞书多维表格( base )里的全部数据表。ref 支持 base 链接或 app_token。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"ref": map[string]any{"type": "string", "description": "多维表格链接或 app_token"},
				},
				"required": []string{"ref"},
			},
		}},
		{Type: "function", Function: llm.FunctionDef{
			Name:        "feishu_bitable_fields",
			Description: "列出多维表格某个数据表的全部字段(名称/类型),用于构造记录查询条件。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"ref":      map[string]any{"type": "string", "description": "多维表格链接或 app_token"},
					"table_id": map[string]any{"type": "string", "description": "数据表 ID,如 tblxxx"},
				},
				"required": []string{"ref", "table_id"},
			},
		}},
		{Type: "function", Function: llm.FunctionDef{
			Name:        "feishu_bitable_records",
			Description: "搜索多维表格数据表的记录。filter 结构与飞书记录筛选 API 一致(conjunction+conditions),不确定字段结构时先用 feishu_bitable_fields 查看。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"ref":         map[string]any{"type": "string", "description": "多维表格链接或 app_token"},
					"table_id":    map[string]any{"type": "string", "description": "数据表 ID"},
					"field_names": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "可选,只返回这些字段"},
					"filter":      map[string]any{"type": "object", "description": "可选,筛选条件,如 {\"conjunction\":\"and\",\"conditions\":[{\"field_name\":\"状态\",\"operator\":\"is\",\"value\":[\"已完成\"]}]}"},
					"limit":       map[string]any{"type": "integer", "description": "返回条数,默认 20,最大 100"},
				},
				"required": []string{"ref", "table_id"},
			},
		}},
	}
}

// Call 执行一个飞书工具
func (t *Tools) Call(ctx context.Context, name string, args map[string]any) (string, error) {
	switch name {
	case "feishu_read_doc":
		return t.readDoc(ctx, strArg(args["ref"]))
	case "feishu_get_wiki_node":
		return t.wikiNode(ctx, strArg(args["ref"]))
	case "feishu_sheet_meta":
		return t.sheetMeta(ctx, strArg(args["ref"]))
	case "feishu_sheet_find":
		return t.sheetFind(ctx, args)
	case "feishu_bitable_tables":
		return t.bitableTables(ctx, strArg(args["ref"]))
	case "feishu_bitable_fields":
		return t.bitableFields(ctx, strArg(args["ref"]), strArg(args["table_id"]))
	case "feishu_bitable_records":
		return t.bitableRecords(ctx, args)
	}
	return "", fmt.Errorf("未知工具: %s", name)
}

// readDoc 读取文档全文;wiki 链接先解析出真实文档 token
func (t *Tools) readDoc(ctx context.Context, ref string) (string, error) {
	r, ok := ParseLink(ref)
	if !ok || r.Token == "" {
		return "", fmt.Errorf("无法从 %q 解析出文档 token", ref)
	}
	docID := r.Token
	switch r.Kind {
	case "wiki":
		node, err := t.wikiNodeObj(ctx, r.Token)
		if err != nil {
			return "", err
		}
		if node.ObjType != nil && *node.ObjType != "docx" {
			return "", fmt.Errorf("知识库节点类型为 %s,暂只支持 docx 类型文档", deref(node.ObjType))
		}
		docID = deref(node.ObjToken)
	case "sheets", "base":
		return "", fmt.Errorf("%s 是表格类资源,请用对应表格工具", r.Kind)
	}
	resp, err := t.api.Docx.V1.Document.RawContent(ctx, larkdocx.NewRawContentDocumentReqBuilder().DocumentId(docID).Build())
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("读取文档失败: code=%d msg=%s(请确认文档已对应用开放)", resp.Code, resp.Msg)
	}
	content := deref(resp.Data.Content)
	title := fmt.Sprintf("文档 %s", docID)
	return t.trunc(title + "\n" + content), nil
}

func (t *Tools) wikiNodeObj(ctx context.Context, token string) (*larkwiki.Node, error) {
	resp, err := t.api.Wiki.Space.GetNode(ctx, larkwiki.NewGetNodeSpaceReqBuilder().Token(token).Build())
	if err != nil {
		return nil, err
	}
	if !resp.Success() {
		return nil, fmt.Errorf("查询知识库节点失败: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return resp.Data.Node, nil
}

func (t *Tools) wikiNode(ctx context.Context, ref string) (string, error) {
	r, ok := ParseLink(ref)
	if !ok || r.Token == "" {
		return "", fmt.Errorf("无法从 %q 解析出 wiki token", ref)
	}
	node, err := t.wikiNodeObj(ctx, r.Token)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("标题: %s\n类型: %s\n文档 token: %s\n(把上面的文档 token 或原链接交给 feishu_read_doc 可读全文)",
		deref(node.Title), deref(node.ObjType), deref(node.ObjToken)), nil
}

func (t *Tools) sheetMeta(ctx context.Context, ref string) (string, error) {
	r, ok := ParseLink(ref)
	if !ok || r.Token == "" {
		return "", fmt.Errorf("无法从 %q 解析出 spreadsheet token", ref)
	}
	get, err := t.api.Sheets.Spreadsheet.Get(ctx, larksheets.NewGetSpreadsheetReqBuilder().SpreadsheetToken(r.Token).Build())
	if err != nil {
		return "", err
	}
	if !get.Success() {
		return "", fmt.Errorf("获取表格失败: code=%d msg=%s(请确认表格已对应用开放)", get.Code, get.Msg)
	}
	q, err := t.api.Sheets.SpreadsheetSheet.Query(ctx, larksheets.NewQuerySpreadsheetSheetReqBuilder().SpreadsheetToken(r.Token).Build())
	if err != nil {
		return "", err
	}
	if !q.Success() {
		return "", fmt.Errorf("获取工作表列表失败: code=%d msg=%s", q.Code, q.Msg)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "表格 %s,共 %d 个工作表:\n", deref(get.Data.Spreadsheet.Title), len(q.Data.Sheets))
	for _, s := range q.Data.Sheets {
		fmt.Fprintf(&b, "- sheet_id=%s 标题=%s 位置=%d\n", deref(s.SheetId), deref(s.Title), derefInt(s.Index))
	}
	return b.String(), nil
}

func (t *Tools) sheetFind(ctx context.Context, args map[string]any) (string, error) {
	r, ok := ParseLink(strArg(args["ref"]))
	if !ok || r.Token == "" {
		return "", fmt.Errorf("无法解析 spreadsheet token")
	}
	sheetID, kw := strArg(args["sheet_id"]), strArg(args["find"])
	if sheetID == "" || kw == "" {
		return "", fmt.Errorf("sheet_id 和 find 必填")
	}
	cond := &larksheets.FindCondition{
		MatchCase:       boolPtr(boolArg(args["match_case"])),
		MatchEntireCell: boolPtr(boolArg(args["match_entire"])),
		SearchByRegex:   boolPtr(boolArg(args["search_by_regex"])),
	}
	if rg := strArg(args["range"]); rg != "" {
		cond.Range = &rg
	}
	resp, err := t.api.Sheets.SpreadsheetSheet.Find(ctx,
		larksheets.NewFindSpreadsheetSheetReqBuilder().
			SpreadsheetToken(r.Token).
			SheetId(sheetID).
			Find(&larksheets.Find{Find: &kw, FindCondition: cond}).
			Build())
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("查找失败: code=%d msg=%s", resp.Code, resp.Msg)
	}
	res := resp.Data.FindResult
	if res == nil || (len(res.MatchedCells) == 0 && len(res.MatchedFormulaCells) == 0) {
		return fmt.Sprintf("工作表 %s 中没有找到 %q", sheetID, kw), nil
	}
	return fmt.Sprintf("找到 %d 个匹配单元格:\n%v\n含公式匹配: %v",
		len(res.MatchedCells), res.MatchedCells, res.MatchedFormulaCells), nil
}

func (t *Tools) bitableTables(ctx context.Context, ref string) (string, error) {
	r, ok := ParseLink(ref)
	if !ok || r.Token == "" {
		return "", fmt.Errorf("无法从 %q 解析出 app_token", ref)
	}
	resp, err := t.api.Bitable.AppTable.List(ctx,
		larkbitable.NewListAppTableReqBuilder().AppToken(r.Token).PageSize(100).Build())
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("列数据表失败: code=%d msg=%s(请确认多维表格已对应用开放)", resp.Code, resp.Msg)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "多维表格 %s,共 %d 个数据表:\n", r.Token, len(resp.Data.Items))
	for _, it := range resp.Data.Items {
		fmt.Fprintf(&b, "- table_id=%s 名称=%s\n", deref(it.TableId), deref(it.Name))
	}
	if r.TableID != "" {
		fmt.Fprintf(&b, "(链接中携带 table_id=%s)\n", r.TableID)
	}
	return b.String(), nil
}

func (t *Tools) bitableFields(ctx context.Context, ref, tableID string) (string, error) {
	r, ok := ParseLink(ref)
	if !ok || r.Token == "" {
		return "", fmt.Errorf("无法从 %q 解析出 app_token", ref)
	}
	if tableID == "" {
		tableID = r.TableID
	}
	if tableID == "" {
		return "", fmt.Errorf("缺少 table_id(可先用 feishu_bitable_tables 获取,或用带 ?table= 的链接)")
	}
	resp, err := t.api.Bitable.AppTableField.List(ctx,
		larkbitable.NewListAppTableFieldReqBuilder().AppToken(r.Token).TableId(tableID).Build())
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("列字段失败: code=%d msg=%s", resp.Code, resp.Msg)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "数据表 %s 共 %d 个字段:\n", tableID, len(resp.Data.Items))
	for _, f := range resp.Data.Items {
		fmt.Fprintf(&b, "- %s (类型 %d)\n", deref(f.FieldName), derefInt(f.Type))
	}
	return b.String(), nil
}

func (t *Tools) bitableRecords(ctx context.Context, args map[string]any) (string, error) {
	r, ok := ParseLink(strArg(args["ref"]))
	if !ok || r.Token == "" {
		return "", fmt.Errorf("无法解析 app_token")
	}
	tableID := strArg(args["table_id"])
	if tableID == "" {
		tableID = r.TableID
	}
	if tableID == "" {
		return "", fmt.Errorf("缺少 table_id")
	}
	limit := intArg(args["limit"])
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	body := &larkbitable.SearchAppTableRecordReqBody{}
	if raw, err := json.Marshal(args); err == nil && len(raw) > 2 {
		_ = json.Unmarshal(raw, body) // field_names/filter/sort 与请求参数同名,直接映射;解析失败就当没传
	}
	resp, err := t.api.Bitable.AppTableRecord.Search(ctx,
		larkbitable.NewSearchAppTableRecordReqBuilder().
			AppToken(r.Token).TableId(tableID).Limit(limit).
			Body(body).Build())
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("搜索记录失败: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if len(resp.Data.Items) == 0 {
		return "没有匹配的记录", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "共返回 %d 条记录:\n", len(resp.Data.Items))
	for i, rec := range resp.Data.Items {
		fmt.Fprintf(&b, "--- 记录 %d ---\n", i+1)
		for k, v := range rec.Fields {
			fmt.Fprintf(&b, "%s: %s\n", k, formatValue(v))
		}
	}
	return t.trunc(b.String()), nil
}

// formatValue 把字段值(可能是字符串/数字/复杂对象)压成一行文本
func formatValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", x), "0"), ".")
	case nil:
		return ""
	default:
		if b, err := json.Marshal(x); err == nil {
			return string(b)
		}
		return fmt.Sprintf("%v", x)
	}
}

func (t *Tools) trunc(s string) string {
	r := []rune(s)
	if len(r) > maxOut {
		return string(r[:maxOut]) + "\n...(内容过长已截断)"
	}
	return s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefInt(i *int) int {
	if i == nil {
		return 0
	}
	return *i
}

func strArg(v any) string {
	s, _ := v.(string)
	return s
}

func intArg(v any) int {
	if n, ok := v.(float64); ok {
		return int(n)
	}
	return 0
}

func boolArg(v any) bool {
	b, _ := v.(bool)
	return b
}

func boolPtr(b bool) *bool { return &b }
