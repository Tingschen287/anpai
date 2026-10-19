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
type markFilter struct{ next http.Handler }

func (m markFilter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h := i18n.WireHook; h != nil && r.Body != nil && r.Body != http.NoBody {
		// 请求体交给钩子：客户端自己写的文字（标题、任务、汇报、选项）是外部原文，响应里原样回显时不算本地中文。
		if b, err := io.ReadAll(r.Body); err == nil {
			h("request "+r.Method+" "+r.URL.Path, b)
			r.Body = io.NopCloser(bytes.NewReader(b))
		}
	}
	fw := &filterWriter{ResponseWriter: w, neutral: r.Header.Get(i18n.MarkHeader) == i18n.MarkNeutral,
		kind: r.Method + " " + r.URL.Path, status: http.StatusOK}
	m.next.ServeHTTP(fw, r)
}

type filterWriter struct {
	http.ResponseWriter
	neutral bool
	kind    string
	status  int
}

func (f *filterWriter) WriteHeader(code int) {
	f.status = code
	f.ResponseWriter.WriteHeader(code)
}

func (f *filterWriter) Write(p []byte) (int, error) {
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
	if fl, ok := f.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

// Unwrap 让 http.ResponseController 能找到底下的 writer。
func (f *filterWriter) Unwrap() http.ResponseWriter { return f.ResponseWriter }
