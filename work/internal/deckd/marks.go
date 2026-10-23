package deckd

import (
	"bytes"
	"io"
	"net/http"

	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
)

// markFilter 是 deckd 写给客户端的唯一一层出口（n060 段 2 返工 3）。所有响应、SSE 通知都从这里经过：
//
//  1. 打码标记。存下来的文字带着语言无关的中性标记 i18n.RedactMark。
//     请求带 i18n.MarkHeader（界面用 deckclient.NewUI）的，线路上保留标记，界面按当前语言翻译；
//     不带的（deck agent 命令、模型、旧工具）拿到的标记一律换成中文「[已打码]」，和改码之前逐字一样。
//  2. 测试钩子。i18n.WireHook 默认是 nil，生产行为不变；测试里设上以后，每一段写给客户端的响应或通知都交给它检查。
//  3. 界面语言（n060 段 3）。lang 不为 nil 时，每个响应带 i18n.LangHeader：此刻的界面语言。界面进程据此换语言。
type markFilter struct {
	next http.Handler
	lang func() string
}

func (m markFilter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r = neutralRequest(r) // 路径、查询串、Last-Event-ID 里伪造的标记（返工 4）
	if h := i18n.WireHook; h != nil && r.Body != nil && r.Body != http.NoBody {
		// 请求体交给钩子：客户端自己写的文字（标题、任务、汇报、选项）是外部原文，响应里原样回显时不算本地中文。
		if b, err := io.ReadAll(r.Body); err == nil {
			h("request "+r.Method+" "+r.URL.Path, b)
			r.Body = io.NopCloser(bytes.NewReader(b))
		}
	}
	fw := &filterWriter{ResponseWriter: w, neutral: r.Header.Get(i18n.MarkHeader) == i18n.MarkNeutral,
		kind: r.Method + " " + r.URL.Path, status: http.StatusOK, lang: m.lang}
	m.next.ServeHTTP(fw, r)
	fw.stampLang() // 没写过响应体的（空 200）在处理器返回后补上
}

type filterWriter struct {
	http.ResponseWriter
	neutral bool
	kind    string
	status  int
	lang    func() string
	stamped bool
}

// stampLang 在响应头写出去之前的最后一刻，把此刻的界面语言写进 i18n.LangHeader（只写一次）。
// 不在请求一进来就取：PUT /v1/settings 保存了 ui_lang 之后，这次响应必须带新语言，不能带请求进来那一刻的旧语言（返工 2，M2）。
func (f *filterWriter) stampLang() {
	if f.stamped || f.lang == nil {
		return
	}
	f.stamped = true
	f.ResponseWriter.Header().Set(i18n.LangHeader, f.lang())
}

func (f *filterWriter) WriteHeader(code int) {
	f.status = code
	f.stampLang()
	f.ResponseWriter.WriteHeader(code)
}

func (f *filterWriter) Write(p []byte) (int, error) {
	f.stampLang()
	if h := i18n.WireHook; h != nil {
		h(f.kind+" "+http.StatusText(f.status), p)
	}
	if !f.neutral && i18n.HasMark(string(p)) {
		if _, err := f.ResponseWriter.Write([]byte(i18n.PresentZH(string(p)))); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	return f.ResponseWriter.Write(p)
}

func (f *filterWriter) Flush() {
	f.stampLang()
	if fl, ok := f.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

// Unwrap 让 http.ResponseController 能找到底下的 writer。
func (f *filterWriter) Unwrap() http.ResponseWriter { return f.ResponseWriter }
