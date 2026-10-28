package deckui

import (
	"context"
	"fmt"
	"image"
	"image/draw"
	"os"
	"time"

	"github.com/Tingschen287/terminal-deck/work/internal/dashquota"
	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
	"github.com/Tingschen287/terminal-deck/work/internal/logo"
	"github.com/Tingschen287/terminal-deck/work/internal/panels"
)

// QuotaFrame 画额度面板的一屏（n056）：文字由 panels.QuotaLines 给出，sixel 模式下在每个来源那一块的第 1 列贴 logo，
// 图高等于这一块的行数、logo 在其中垂直居中。err 非空时只写原因（不显示旧数据冒充新数据）。
// sixelOf 给出某个工具占 rows 行的 logo sixel（空表示不画）。
func QuotaFrame(qs []dashquota.ToolQuota, err error, w, h int, mode string, sixelOf func(tool string, rows int) string) Frame {
	if err != nil {
		// 错误文本可能带上游内容：显示前清洗（同 n050 评审 B7）
		return Frame{Lines: []string{"\x1b[1m" + T("quota.title") + "\x1b[0m", "\x1b[38;5;203m" + T("ui.loadfail") + oneLine(deckstore.Display(deckclient.Show(err), 300)) + "\x1b[0m"}}
	}
	lines, logos := panels.QuotaLines(qs, w, mode)
	var ims []Image
	for _, lg := range logos {
		if lg.Row+lg.Rows > h { // 整块都在屏上才画
			continue
		}
		if d := sixelOf(lg.Tool, lg.Rows); d != "" {
			ims = append(ims, Image{Row: lg.Row, Col: 1, Data: d})
		}
	}
	return Frame{Lines: lines, Images: ims}
}

// RunQuota 跑额度面板：整屏帧加图（与侧栏同一画法，bubbletea 画不了 sixel），每 2 秒向 deckd 取一次；q 或 Ctrl-C 退出。
// modeOf 给出布局当前的 logo 画法（每次取数时读，变了就切换，n056 评审 1 B2）；nil 时按 logo.Mode()。
func RunQuota(c *deckclient.Client, modeOf func() string) error {
	if modeOf == nil {
		modeOf = logo.Mode
	}
	d, err := openTerm()
	if err != nil {
		return err
	}
	defer d.close()
	mode := modeOf()
	cw, ch := cellSize()
	cache := map[string]string{}
	sixelOf := func(tool string, rows int) string {
		im := logo.For(tool)
		if im == nil {
			return ""
		}
		w, h := cw, ch
		if w <= 0 || h <= 0 {
			w, h = 10, 20
		}
		key := fmt.Sprintf("%s/%dx%d/%d/%s", tool, w, h, rows, logo.BackgroundKey())
		if v, ok := cache[key]; ok {
			return v
		}
		v := quotaSixel(im, w, h, rows)
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
			if m := modeOf(); m != mode {
				mode, last = m, ""
				draw()
			}
			fetch()
		}
	}
}

// quotaLogoSize 是额度面板一块的 logo 画布：side 是 logo 的边长（宽也是 side），height 是整块的高（rows 行，logo 在里面垂直居中）。
// 合成画法（iTerm2）下 side 与 height 一起取 6 的整数倍且不超过预留的像素：以前只取整 height，两行块 side=64、height=60 时
// PadVertical 因 height<=side 原样返回方图，再被压成 64×60，纵向压扁 6%（Codex 复审 2 的 R2-3）。现在 side 也取整，
// height ≥ side 恒成立（rows ≥ 2，FitHeight 单调），所以 logo 始终是正方形、居中、不越出预留行。透明画法（WT）原样不变。
func quotaLogoSize(w, h, rows int) (side, height int) {
	side = min(panels.QuotaLogoRows*h, panels.QuotaLogoCols*w)
	height = max(rows, panels.QuotaLogoRows) * h
	return logo.FitHeight(side), logo.FitHeight(height)
}

// quotaSixel 画额度面板一块的 logo（rows 行高）。
func quotaSixel(im image.Image, w, h, rows int) string {
	side, height := quotaLogoSize(w, h, rows)
	return logo.Sixel(PadVertical(im, side, height), side, height)
}

// PadVertical 把边长 side 的 logo 放进 side × height 像素的透明画布、垂直居中（按 img 自己的分辨率等比放大画布，
// 交给 logo.Sixel 缩到 side × height）。额度面板一块有三行时，两行高的 logo 才能和这一块居中对齐（n056）。
func PadVertical(img image.Image, side, height int) image.Image {
	if height <= side || side <= 0 {
		return img
	}
	b := img.Bounds()
	ch := b.Dy() * height / side
	canvas := image.NewNRGBA(image.Rect(0, 0, b.Dx(), ch))
	top := (ch - b.Dy()) / 2
	draw.Draw(canvas, image.Rect(0, top, b.Dx(), top+b.Dy()), img, b.Min, draw.Src)
	return canvas
}
