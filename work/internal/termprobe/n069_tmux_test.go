package termprobe

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Tingschen287/terminal-deck/work/internal/logo"
)

var tmuxBin = findTmux()

func findTmux() string {
	if p, err := exec.LookPath("tmux"); err == nil {
		return p
	}
	return "/Users/joy/.local/bin/tmux"
}

// 一个自己的 tmux server（-S 专用 socket）+ 一个接在 pty 上的 client，抓 client 往外层终端写的全部字节。
type tmuxRig struct {
	t      *testing.T
	sock   string
	master *os.File
	cmd    *exec.Cmd
	mu     sync.Mutex
	out    bytes.Buffer
}

func (r *tmuxRig) tmux(args ...string) (string, error) {
	c := exec.Command(tmuxBin, append([]string{"-S", r.sock}, args...)...)
	c.Env = append(os.Environ(), "TERM=xterm-256color")
	b, err := c.CombinedOutput()
	return string(b), err
}

func (r *tmuxRig) captured() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.out.String()
}

func newRig(t *testing.T, name, conf, paneCmd string, cols, rows, xpix, ypix int) *tmuxRig {
	t.Helper()
	dir := os.Getenv("TMUX_TMPDIR")
	if dir == "" {
		t.Skip("要设 TMUX_TMPDIR")
	}
	r := &tmuxRig{t: t, sock: filepath.Join(dir, name+".sock")}
	os.Remove(r.sock)
	confPath := filepath.Join(dir, name+".conf")
	os.WriteFile(confPath, []byte(conf), 0o600)
	if out, err := r.tmux("-f", confPath, "new-session", "-d", "-s", "deck", paneCmd); err != nil {
		t.Fatalf("起不了 tmux：%v %s", err, out)
	}
	t.Cleanup(func() { r.tmux("kill-server"); os.Remove(r.sock) })
	m, s, err := openPty()
	if err != nil {
		t.Skip(err)
	}
	r.master = m
	ws := &unix.Winsize{Row: uint16(rows), Col: uint16(cols), Xpixel: uint16(xpix), Ypixel: uint16(ypix)}
	if err := unix.IoctlSetWinsize(int(s.Fd()), unix.TIOCSWINSZ, ws); err != nil {
		t.Fatal(err)
	}
	c := exec.Command(tmuxBin, "-S", r.sock, "attach", "-t", "deck")
	c.Env = append(os.Environ(), "TERM=xterm-256color")
	c.Stdin, c.Stdout, c.Stderr = s, s, s
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	r.cmd = c
	s.Close()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := m.Read(buf)
			if n > 0 {
				r.mu.Lock()
				r.out.Write(buf[:n])
				r.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { c.Process.Kill(); c.Wait(); m.Close() })
	time.Sleep(300 * time.Millisecond)
	// 像真终端那样改一次窗口大小：tmux 的窗口格子像素只在 client 改大小时更新（deck 的 keepPixels 也是靠 SIGWINCH）
	unix.IoctlSetWinsize(int(m.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: uint16(rows), Col: uint16(cols + 1), Xpixel: uint16(xpix + xpix/cols), Ypixel: uint16(ypix)})
	time.Sleep(300 * time.Millisecond)
	m.Write([]byte("\r")) // 让 window-size latest 认这个 client（有过输入）
	time.Sleep(300 * time.Millisecond)
	return r
}

func (r *tmuxRig) waitFor(substr string, d time.Duration) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if strings.Contains(r.captured(), substr) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// 极简 sixel 解码：返回光栅属性 w、h、每个像素的寄存器号（-1 没画）、寄存器定义（百分比）。
func decode(s string) (w, h int, grid [][]int, colors map[int][3]int, err error) {
	i := strings.Index(s, "q")
	if i < 0 {
		return 0, 0, nil, nil, fmt.Errorf("没有 q")
	}
	body := strings.TrimSuffix(s[i+1:], "\x1b\\")
	if _, e := fmt.Sscanf(body, "\"%d;%d;%d;%d", new(int), new(int), &w, &h); e != nil {
		return 0, 0, nil, nil, fmt.Errorf("光栅属性：%v（%.30q）", e, body)
	}
	body = body[strings.Index(body, "#"):]
	maxRows := ((h + 5) / 6) * 6
	grid = make([][]int, maxRows)
	for y := range grid {
		grid[y] = make([]int, w)
		for x := range grid[y] {
			grid[y][x] = -1
		}
	}
	colors = map[int][3]int{}
	cur, x, band := 0, 0, 0
	num := func(i int) (int, int) {
		j := i
		for j < len(body) && body[j] >= '0' && body[j] <= '9' {
			j++
		}
		n, _ := strconv.Atoi(body[i:j])
		return n, j
	}
	put := func(c byte, n int) {
		bits := c - 63
		for k := 0; k < n; k++ {
			for b := 0; b < 6; b++ {
				if bits&(1<<b) != 0 && band+b < len(grid) && x < w {
					grid[band+b][x] = cur
				}
			}
			x++
		}
	}
	for i := 0; i < len(body); {
		switch c := body[i]; {
		case c == '#':
			n, j := num(i + 1)
			if j < len(body) && body[j] == ';' {
				var a, r, g, b int
				fmt.Sscanf(body[j:], ";%d;%d;%d;%d", &a, &r, &g, &b)
				colors[n] = [3]int{r, g, b}
				for j < len(body) && (body[j] == ';' || (body[j] >= '0' && body[j] <= '9')) {
					j++
				}
			}
			cur, i = n, j
		case c == '!':
			n, j := num(i + 1)
			put(body[j], n)
			i = j + 1
		case c == '$':
			x, i = 0, i+1
		case c == '-':
			x, band, i = 0, band+6, i+1
		case c >= 63 && c <= 126:
			put(c, 1)
			i++
		default:
			i++
		}
	}
	return w, h, grid, colors, nil
}

func dcsOf(out string) []string {
	var res []string
	for {
		i := strings.Index(out, "\x1bP")
		if i < 0 {
			return res
		}
		j := strings.Index(out[i:], "\x1b\\")
		if j < 0 {
			return res
		}
		res = append(res, out[i:i+j+2])
		out = out[i+j+2:]
	}
}

func catPane(t *testing.T, name string, payload string) string {
	p := filepath.Join(os.Getenv("TMUX_TMPDIR"), name+".six")
	os.WriteFile(p, []byte(payload), 0o600)
	return "sleep 1.5; cat " + p + "; sleep 60"
}

func solid(w, h int) image.Image {
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if (x/4+y/4)%2 == 0 {
				im.SetNRGBA(x, y, color.NRGBA{180, 245, 70, 255})
			}
		}
	}
	return im
}

// n069 样张 9：经 tmux 的 sixel 在 iTerm2 上每张图下面多一条前景色横条。原因两条（隔离 tmux 3.4 复现，owner 机器上 tmux 用默认格子 16×32）：
//  1. sixel 数据按 6 行一段，32 像素高的图数据是 36 行，超过 tmux 认为的一格（32），被占成两格并补齐；
//  2. 补出来的像素「没画」，iTerm2 把没画的涂成最后定义的颜色——tmux 再编码时按寄存器号从小到大定义，最后一个是前景。
//
// 修法：图高取 6 的整数倍且不超过这几行（logo.FitHeight）；背景色放在最后一个寄存器（SixelOn）。
// 这条测试把图（侧栏标题一行高、会话两行高，用产品的尺寸算法）真的经过 tmux 3.4，解码 tmux 送出的 sixel，
// 按 iTerm2 的填法（没画的取最后定义的颜色）渲染，断言：补出的像素全是背景色；图没有多占一行；源图范围内没有没画的像素。
func TestN069TmuxReencodeLeavesNoForegroundStripes(t *testing.T) {
	bg := color.RGBA{16, 16, 16, 255}
	logo.SetBackground(&bg)
	t.Cleanup(func() { logo.SetBackground(nil) })
	conf := "set -g status off\nset -g window-size latest\nset -as terminal-features ',*:sixel'\n"
	bgPct := [3]int{6, 6, 6} // (16,16,16) 的百分比
	// 客户端报的格子像素：owner 机器上 tmux 用的是默认 16×32（客户端像素没被 tmux 采用）；另测真实的 16×38 与别的格子
	for _, cell := range []struct{ cw, ch int }{{16, 32}, {16, 38}, {8, 18}} {
		cols, rows := 100, 30
		for _, c := range []struct {
			name           string
			tool           string
			cellsH, cellsW int
		}{{"title1row", "deck", 1, 2}, {"session2rows", "claude", 2, 4}, {"session2rows-deck", "deck", 2, 4}} {
			side := logo.FitHeight(min(c.cellsH*cell.ch, c.cellsW*cell.cw))
			im := logo.Deck()
			if c.tool != "deck" {
				im = logo.For(c.tool)
			}
			name := fmt.Sprintf("n69_%dx%d_%s", cell.cw, cell.ch, c.name)
			r := newRig(t, name, conf, catPane(t, name, logo.Sixel(im, side, side)), cols, rows, cols*cell.cw, rows*cell.ch)
			time.Sleep(1200 * time.Millisecond)
			var got []string
			for _, d := range dcsOf(r.captured()) {
				if strings.Contains(d, "q") && strings.Contains(d, "#") {
					got = append(got, d)
				}
			}
			if len(got) == 0 {
				t.Fatalf("%s：tmux 没有往外层输出 sixel", name)
			}
			d := got[len(got)-1]
			w, h, grid, cs, err := decode(d)
			if err != nil {
				t.Fatalf("%s：%v", name, err)
			}
			defs := regexp.MustCompile(`#(\d+);2;(\d+);(\d+);(\d+)`).FindAllStringSubmatch(d, -1)
			var last [3]int
			if len(defs) > 0 {
				m := defs[len(defs)-1]
				last = [3]int{atoi(m[2]), atoi(m[3]), atoi(m[4])}
			}
			if last != bgPct {
				t.Errorf("%s：tmux 送出的流里最后定义的颜色是 %v，应是背景 %v：iTerm2 会把没画的像素涂成它", name, last, bgPct)
			}
			// 图没有多占：声明的高度不超过这几行（按 tmux 实际用的格子；tmux 的格子高是 32 或 38，取较大的算）
			if h > c.cellsH*max(cell.ch, 32) {
				t.Errorf("%s：声明高 %d 超过 %d 行 × %d 像素，多占了下面一行", name, h, c.cellsH, max(cell.ch, 32))
			}
			// 按 iTerm2 的填法渲染：没画的取最后定义的颜色，再看哪里不是背景色、前景有没有越出源图
			fgBelow, unsetInSource := 0, 0
			for y := 0; y < len(grid) && y < h; y++ {
				for x := 0; x < w; x++ {
					reg := grid[y][x]
					if reg < 0 {
						if y < side && x < side {
							unsetInSource++
						}
						continue
					}
					if y >= side || x >= side+cell.cw {
						if cs[reg] != bgPct {
							fgBelow++
						}
					}
				}
			}
			if fgBelow > 0 {
				t.Errorf("%s：源图（%d）之外有 %d 个前景色像素", name, side, fgBelow)
			}
			t.Logf("%s：源 %dx%d → tmux 声明 %dx%d，源图范围内没画 %d 个，最后定义的颜色 %v", name, side, side, w, h, unsetInSource, last)
		}
	}
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }
