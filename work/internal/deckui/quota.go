package deckui

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/dashquota"
	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/logo"
	"github.com/Tingschen287/terminal-deck/work/internal/panels"
)

// QuotaFrame 画额度面板的一屏（n056）：文字由 panels.QuotaLines 给出，sixel 模式下在每个来源名字那两行的第 1 列贴 logo。
// err 非空时只写原因（不显示旧数据冒充新数据）。sixelOf 给出某个工具 logo 的 sixel 数据（空表示不画）。
func QuotaFrame(qs []dashquota.ToolQuota, err error, w, h int, mode string, sixelOf func(tool string) string) Frame {
	if err != nil {
		// 错误文本可能带上游内容：显示前清洗（同 n050 评审 B7）
		return Frame{Lines: []string{"\x1b[1m额度\x1b[0m", "\x1b[38;5;203m取不到：" + oneLine(deckstore.Clean(err.Error(), 300)) + "\x1b[0m"}}
	}
	lines, logos := panels.QuotaLines(qs, w, mode)
	var ims []Image
	for _, lg := range logos {
		if lg.Row+panels.QuotaLogoRows > h { // 两行都在屏上才画
			continue
		}
		if d := sixelOf(lg.Tool); d != "" {
			ims = append(ims, Image{Row: lg.Row, Col: 1, Data: d})
		}
	}
	return Frame{Lines: lines, Images: ims}
}

// RunQuota 跑额度面板：整屏帧加图（与侧栏同一画法，bubbletea 画不了 sixel），每 2 秒向 deckd 取一次；q 或 Ctrl-C 退出。
func RunQuota(c *deckclient.Client) error {
	d, err := openTerm()
	if err != nil {
		return err
	}
	defer d.close()
	mode := logo.Mode()
	cw, ch := cellSize()
	cache := map[string]string{}
	sixelOf := func(tool string) string {
		im := logo.For(tool)
		if im == nil {
			return ""
		}
		w, h := cw, ch
		if w <= 0 || h <= 0 {
			w, h = 10, 20
		}
		key := fmt.Sprintf("%s/%dx%d", tool, w, h)
		if v, ok := cache[key]; ok {
			return v
		}
		side := min(panels.QuotaLogoRows*h, panels.QuotaLogoCols*w)
		v := logo.Sixel(im, side, side)
		cache[key] = v
		return v
	}
	var qs []dashquota.ToolQuota
	var ferr error
	type got struct {
		qs  []dashquota.ToolQuota
		err error
	}
	results := make(chan got, 1)
	busy := false
	// 取数放到后台：deckd 慢时按键与改大小照常响应（n056 自查：原先同步取数最多卡 10 秒）
	fetch := func() {
		if busy {
			return
		}
		busy = true
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var x []dashquota.ToolQuota
			err := c.Get(ctx, "/v1/quota", &x)
			results <- got{x, err}
		}()
	}
	last := ""
	draw := func() {
		w, h := size()
		if out := QuotaFrame(qs, ferr, w, h, mode, sixelOf).Encode(w, h); out != last {
			os.Stdout.WriteString(out)
			last = out
		}
	}
	fetch()
	draw()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case ev, ok := <-d.events:
			if !ok || ev.Key == "q" || ev.Key == "ctrl-c" {
				return nil
			}
		case <-d.resize:
			cw, ch = cellSize()
			last = ""
			draw()
		case r := <-results:
			busy = false
			if ferr = r.err; ferr == nil {
				qs = r.qs
			}
			draw()
		case <-tick.C:
			fetch()
		}
	}
}
