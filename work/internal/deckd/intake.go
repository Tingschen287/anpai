package deckd

import (
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// 外部原文的入口（n060 段 2 返工 4）。
//
// 打码的真标记只由 deckstore.Clean 写；外部来的文字里如果带着同样的私用区字符，就是伪造，
// 必须在它进来的那一刻换掉，不能等到进了通知参数、已经和真标记分不开的时候。
// deckd 收外部文字的口子有两处：
//   - 请求体：readBody 解码之后过 neutralizeValue（所有字符串字段、map 的键和值、嵌套值）；
//   - 路径、查询串和 Last-Event-ID 头：markFilter 在转给路由之前过 neutralRequest。
//
// 外部命令的输出、上游 HTTP 响应、读进来的文件在各自的适配包里同样处理，入口清单见 inventory.md，
// 守卫测试 wirecheck.TestN060ExternalIntakeFilesAreListed 按文件盯着不让新增入口漏掉。

var rawMessageType = reflect.TypeOf(json.RawMessage(nil))

// neutralizeValue 把 v 指向的值里所有字符串里的标记字符换成 U+FFFD。v 必须是指针。
func neutralizeValue(v any) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return
	}
	neutralizeWalk(rv.Elem())
}

func neutralizeWalk(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		if v.CanSet() {
			if s := v.String(); i18n.HasMark(s) {
				v.SetString(i18n.Neutralize(s))
			}
		}
	case reflect.Pointer:
		if !v.IsNil() {
			neutralizeWalk(v.Elem())
		}
	case reflect.Interface:
		if v.IsNil() || !v.CanSet() {
			return
		}
		e := v.Elem()
		cp := reflect.New(e.Type()).Elem()
		cp.Set(e)
		neutralizeWalk(cp)
		v.Set(cp)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			neutralizeWalk(v.Field(i))
		}
	case reflect.Slice:
		if v.Type() == rawMessageType {
			if v.CanSet() && !v.IsNil() {
				v.SetBytes(i18n.NeutralizeBytes(v.Bytes()))
			}
			return
		}
		for i := 0; i < v.Len(); i++ {
			neutralizeWalk(v.Index(i))
		}
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			neutralizeWalk(v.Index(i))
		}
	case reflect.Map:
		if v.IsNil() || !v.CanSet() {
			return
		}
		for _, k := range v.MapKeys() {
			val := v.MapIndex(k)
			cp := reflect.New(val.Type()).Elem()
			cp.Set(val)
			neutralizeWalk(cp)
			nk := k
			if k.Kind() == reflect.String && i18n.HasMark(k.String()) {
				nk = reflect.New(k.Type()).Elem()
				nk.SetString(i18n.Neutralize(k.String()))
				v.SetMapIndex(k, reflect.Value{})
			}
			v.SetMapIndex(nk, cp)
		}
	}
}

// neutralRequest 返回路径、查询串、Last-Event-ID 头里没有伪造标记的请求。本来就没有的原样返回，不复制。
func neutralRequest(r *http.Request) *http.Request {
	rawQuery, _ := url.QueryUnescape(r.URL.RawQuery)
	last := r.Header.Get("Last-Event-ID")
	if !i18n.HasMark(r.URL.Path) && !i18n.HasMark(rawQuery) && !i18n.HasMark(last) {
		return r
	}
	r2 := r.Clone(r.Context())
	if i18n.HasMark(r.URL.Path) {
		r2.URL.Path, r2.URL.RawPath = i18n.Neutralize(r.URL.Path), ""
	}
	if i18n.HasMark(rawQuery) {
		q := r.URL.Query()
		nq := url.Values{}
		for k, vs := range q {
			for _, v := range vs {
				nq.Add(i18n.Neutralize(k), i18n.Neutralize(v))
			}
		}
		r2.URL.RawQuery = nq.Encode()
	}
	if i18n.HasMark(last) {
		r2.Header.Set("Last-Event-ID", i18n.Neutralize(last))
	}
	return r2
}
