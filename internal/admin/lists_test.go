package admin_test

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"tillbox/internal/apptest"
	"tillbox/internal/store"
)

// 后台接口里的列表没有数据时也要返回 []：前端按数组读，收到 null 会直接崩。
// 新增返回列表的接口时，把它加进下面的表。
func TestEmptyListsAreArrays(t *testing.T) {
	e := apptest.New(t)
	a := e.Login()
	c := store.AssistanceCase{Source: "payer", Kind: "missing", Status: "manual", CreatedAt: 1, UpdatedAt: 1}
	if _, err := e.DB().NewInsert().Model(&c).Exec(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path   string
		fields []string
	}{
		{"/web-api/admin/overview", []string{"days", "merchants", "ledgers"}},
		{"/web-api/admin/channels", []string{"items"}},
		{"/web-api/admin/merchants", []string{"items"}},
		{"/web-api/admin/orders", []string{"items"}},
		{"/web-api/admin/orders?is_test=true", []string{"items"}},
		{"/web-api/admin/transactions", []string{"items"}},
		{"/web-api/admin/anomalies", []string{"items"}},
		{"/web-api/admin/ledger/status", []string{"items"}},
		{"/web-api/admin/notifications", []string{"items"}},
		{"/web-api/admin/audit", []string{"items"}},
		{"/web-api/admin/assistance?status=closed", []string{"items"}},
		{"/web-api/admin/assistance/cases/" + strconv.FormatInt(c.ID, 10), []string{"actions"}},
		{"/web-api/admin/operations", []string{"issues", "archives"}},
	} {
		r := a.Call("GET", tc.path, nil)
		expect(t, r, 200, "")
		var body map[string]json.RawMessage
		if err := json.Unmarshal(r.Body, &body); err != nil {
			t.Fatalf("%s：%v", tc.path, err)
		}
		for _, f := range tc.fields {
			if v := body[f]; len(v) == 0 || v[0] != '[' {
				t.Errorf("%s 的 %s 应是数组，实际是 %s", tc.path, f, v)
			}
		}
	}
}
