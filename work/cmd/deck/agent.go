package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckd"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// deck agent：主 agent 与子 agent 的工具（架构稿 §5，n051）。都经 deckd，输出 JSON；身份由 deckd 按调用进程判定。
//
//	deck agent whoami
//	deck agent spawn --tier <档位> | --model <模型> --title <标题> --task-file <文件|-> [--dir 目录] [--graph-node 节点]
//	                 [--exception 决定 --request-id 原请求 id]
//	deck agent list | output <id> | send <id> --text <内容>
//	deck agent escalate --kind <cost|config|push|disagree|deviation> --summary <说明> [--options a,b] [--recommend x] [--scope k=v,…]
//	deck agent decisions
//	deck agent report [--done] --text <内容>
//	deck agent start-main --project <项目>        （owner）
//
// 退出码：0 完成；3 等 owner 批准（输出里有决定与下一步）；1 出错。
func agentCmd(dirs deckstore.Dirs, args []string) {
	if len(args) < 1 {
		agentUsage()
	}
	c := deckclient.New(deckd.SocketPath(dirs))
	ctx := context.Background()
	sub, rest := args[0], args[1:]
	var out any
	var err error
	switch sub {
	case "whoami":
		err = c.Get(ctx, "/v1/whoami", &out)
	case "spawn":
		out, err = agentSpawn(ctx, c, rest)
	case "list":
		err = c.Get(ctx, "/v1/sessions", &out)
	case "output":
		id := needArg(rest, "会话 id")
		err = c.Get(ctx, "/v1/agents/"+url.PathEscape(id)+"/output", &out)
	case "send":
		id := needArg(rest, "会话 id")
		fs := flag.NewFlagSet("send", flag.ExitOnError)
		text := fs.String("text", "", "内容")
		file := fs.String("text-file", "", "从文件读内容（- 为标准输入）")
		fs.Parse(rest[1:])
		body := textArg(*text, *file)
		err = c.Do(ctx, "POST", "/v1/agents/"+url.PathEscape(id)+"/send", map[string]string{"text": body}, &out)
	case "escalate":
		out, err = agentEscalate(ctx, c, rest)
	case "decisions":
		err = c.Get(ctx, "/v1/decisions", &out)
	case "report":
		fs := flag.NewFlagSet("report", flag.ExitOnError)
		done := fs.Bool("done", false, "做完了（转告主 agent）")
		text := fs.String("text", "", "内容")
		file := fs.String("text-file", "", "从文件读内容（- 为标准输入）")
		fs.Parse(rest)
		err = c.Do(ctx, "POST", "/v1/reports", map[string]any{"done": *done, "text": textArg(*text, *file)}, &out)
	case "start-main":
		fs := flag.NewFlagSet("start-main", flag.ExitOnError)
		project := fs.String("project", "", "项目 id")
		fs.Parse(rest)
		if *project == "" {
			fail("要给 --project")
		}
		err = c.Do(ctx, "POST", "/v1/projects/"+url.PathEscape(*project)+"/main", map[string]string{"request_id": newRequestID()}, &out)
	default:
		agentUsage()
	}
	if err != nil {
		json.NewEncoder(os.Stderr).Encode(map[string]string{"error": err.Error()})
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(out)
	if m, ok := out.(map[string]any); ok && m["pending"] != nil {
		os.Exit(3) // 等 owner 批准
	}
}

func agentUsage() {
	fmt.Fprintln(os.Stderr, "用法：deck agent whoami|spawn|list|output|send|escalate|decisions|report|start-main …（deck agent <子命令> -h 看参数）")
	os.Exit(2)
}

func needArg(args []string, what string) string {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		fail("缺少%s", what)
	}
	return args[0]
}

// textArg 取 --text 或 --text-file（- 为标准输入）。
func textArg(text, file string) string {
	if file == "" {
		return text
	}
	b, err := readFileOrStdin(file)
	if err != nil {
		fail("%v", err)
	}
	return string(b)
}

func readFileOrStdin(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	}
	return os.ReadFile(path)
}

// newRequestID 每次运行都是新的启动请求 id：启动失败或结果不明后重试要用新 id（README 的预留合同）。
func newRequestID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "req-" + hex.EncodeToString(b)
}

// agentSpawn 按项目设置在本地解析 agent、档位与模型（与 deckd 同一套规则），算好任务与启动参数的 sha256，再请 deckd 开。
// 工作目录先按字节检查（规整的绝对路径、≤300 字节），不合规就不发请求。
func agentSpawn(ctx context.Context, c *deckclient.Client, args []string) (any, error) {
	fs := flag.NewFlagSet("spawn", flag.ExitOnError)
	tier := fs.String("tier", "", "档位：economy、balanced、frontier")
	model := fs.String("model", "", "具体模型（与 --tier 二选一）")
	agent := fs.String("agent", "", "agent id（可空，按档位或模型解析）")
	title := fs.String("title", "", "标题")
	taskFile := fs.String("task-file", "", "任务正文文件（- 为标准输入）")
	dir := fs.String("dir", "", "工作目录（默认项目目录）")
	graphNode := fs.String("graph-node", "", "走 Graph 通道时的节点")
	exception := fs.String("exception", "", "owner 已批准的 cost 决定 id（超额例外）")
	reqID := fs.String("request-id", "", "启动请求 id（只在带 --exception 重试同一请求时给；默认每次新生成）")
	project := fs.String("project", "", "项目（owner 用；主 agent 不用给）")
	fs.Parse(args)
	if *title == "" || *taskFile == "" {
		return nil, errors.New("要给 --title 与 --task-file")
	}
	if *exception != "" && *reqID == "" {
		return nil, errors.New("带 --exception 重试时要给原来的 --request-id（批准绑定的是那一个请求）")
	}
	task, err := readFileOrStdin(*taskFile)
	if err != nil {
		return nil, err
	}
	var who struct {
		Role    string `json:"role"`
		Project string `json:"project"`
	}
	if err := c.Get(ctx, "/v1/whoami", &who); err != nil {
		return nil, err
	}
	proj := *project
	if who.Role == "main" {
		proj = who.Project
	}
	var set deckstore.Settings
	if err := c.Get(ctx, "/v1/settings", &set); err != nil {
		return nil, err
	}
	var res deckstore.Resolved
	if *model != "" {
		res, err = set.ResolveModel(proj, *model)
	} else {
		res, err = set.ResolveTier(proj, deckstore.Tier(*tier))
	}
	if err != nil {
		return nil, err
	}
	p, _ := set.Project(proj)
	d := *dir
	if d == "" {
		d = p.Path
	}
	if err := deckstore.CheckPath(d); err != nil {
		return nil, fmt.Errorf("工作目录：%w", err)
	}
	id := *reqID
	if id == "" {
		id = newRequestID()
	}
	body := map[string]string{"project": *project, "agent": *agent, "title": *title, "task": string(task), "dir": d,
		"request_id": id, "task_sha256": deckd.TaskSHA256(string(task)),
		"args_sha256": deckd.SpawnArgsSHA256(res.Agent.ID, string(res.Tier), res.Model, *title, d, *graphNode),
		"graph_node":  *graphNode, "exception": *exception}
	if *model != "" {
		body["model"] = *model
	} else {
		body["tier"] = *tier
	}
	var out map[string]any
	if err := c.Do(ctx, "POST", "/v1/agents", body, &out); err != nil {
		return nil, err
	}
	if out["pending"] == deckstore.KindCost {
		dec, _ := out["decision"].(map[string]any)
		out["retry"] = fmt.Sprintf("owner 批准后运行同一条命令，并加 --request-id %s --exception %v", id, dec["id"])
	}
	out["request_id"] = id
	return out, nil
}

func agentEscalate(ctx context.Context, c *deckclient.Client, args []string) (any, error) {
	fs := flag.NewFlagSet("escalate", flag.ExitOnError)
	kind := fs.String("kind", "", "cost、config、push、disagree、deviation")
	summary := fs.String("summary", "", "说明")
	options := fs.String("options", "", "额外的具体选项，逗号分隔（不同意 / 停止由 deck 自动加上）")
	recommend := fs.String("recommend", "", "推荐的选项")
	scope := fs.String("scope", "", "k=v,… 绑定被批准的执行对象")
	fs.Parse(args)
	body := map[string]any{"kind": *kind, "summary": *summary, "recommend": *recommend}
	if *options != "" {
		body["options"] = strings.Split(*options, ",")
	}
	if *scope != "" {
		sc := map[string]string{}
		for _, kv := range strings.Split(*scope, ",") {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return nil, fmt.Errorf("--scope 要写成 k=v：%q", kv)
			}
			sc[k] = v
		}
		body["scope"] = sc
	}
	var out any
	err := c.Do(ctx, "POST", "/v1/decisions", body, &out)
	return out, err
}

// projectCmd：owner 新建项目（deck project add）。读出整份设置、加一个项目、整份写回（deckd 校验）。
func projectCmd(dirs deckstore.Dirs, args []string) {
	if len(args) < 1 || args[0] != "add" {
		fmt.Fprintln(os.Stderr, "用法：deck project add --id <id> --name <名字> --path <目录> --main <agent:档位> [--agents a,b] [--model 档位=模型,…]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("project add", flag.ExitOnError)
	id := fs.String("id", "", "项目 id")
	name := fs.String("name", "", "名字")
	path := fs.String("path", "", "项目目录（规整的绝对路径）")
	mainAgent := fs.String("main", "claude:frontier", "主 agent 与档位")
	agents := fs.String("agents", "", "选用的 agent，逗号分隔（默认只有主 agent）")
	models := fs.String("model", "", "档位=模型，逗号分隔（默认取主 agent 在该档位的第一个模型）")
	fs.Parse(args[1:])
	c := deckclient.New(deckd.SocketPath(dirs))
	ctx := context.Background()
	var set deckstore.Settings
	if err := c.Get(ctx, "/v1/settings", &set); err != nil {
		fail("%v", err)
	}
	ma, tier, _ := strings.Cut(*mainAgent, ":")
	p := deckstore.ProjectDef{ID: *id, Name: *name, Path: *path, Agents: []string{ma}, Models: map[deckstore.Tier]string{},
		MainAgent: deckstore.MainAgent{Agent: ma, Tier: deckstore.Tier(tier)}, Envelope: deckstore.DefaultEnvelope(*id)}
	if *agents != "" {
		p.Agents = strings.Split(*agents, ",")
	}
	if *models != "" {
		for _, kv := range strings.Split(*models, ",") {
			k, v, _ := strings.Cut(kv, "=")
			p.Models[deckstore.Tier(k)] = v
		}
	} else if a, ok := set.Agent(ma); ok {
		for _, m := range a.Models {
			if m.Tier == deckstore.Tier(tier) {
				p.Models[m.Tier] = m.ID
				break
			}
		}
	}
	set.Projects = append(set.Projects, p)
	var out any
	if err := c.Do(ctx, "PUT", "/v1/settings", map[string]any{"agents": set.Agents, "projects": set.Projects}, &out); err != nil {
		fail("%v", err)
	}
	json.NewEncoder(os.Stdout).Encode(p)
}
