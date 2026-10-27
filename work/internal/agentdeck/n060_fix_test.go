package agentdeck

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/i18n"
	"github.com/Tingschen287/terminal-deck/work/internal/wirecheck"
)

func TestN060DeliveryDetailIsNestedCode(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	err := sendResult("s1", []byte(`{"submitted":false,"delivery":"unverified"}`), nil, false)
	zh := err.Error()
	if !strings.Contains(zh, "上游成功退出") || !strings.Contains(zh, "delivery=unverified") {
		t.Fatalf("中文送达详情变了：%q", zh)
	}
	code, args, ok := i18n.Wire(err)
	if !ok {
		t.Fatal("本地送达详情应有码")
	}
	i18n.SetLang(i18n.EN)
	en := deckclient.Text(code, args, zh)
	if strings.Contains(en, "上游") || !strings.Contains(en, "upstream exited successfully") || !strings.Contains(en, "delivery=unverified") {
		t.Fatalf("英文应翻译本地详情：%q", en)
	}

	wirecheck.External("目录信任确认") // 夹具里上游自己说的话，恰好和本项目一条码表文字相同
	raw := sendResult("s1", []byte(`{"delivery":"typed","error":"目录信任确认"}`), errors.New("upstream"), false)
	if !strings.Contains(raw.Error(), "目录信任确认") {
		t.Fatalf("外部原文应原样留下：%q", raw.Error())
	}
}

func TestN060IdentityListUsesLangSep(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	err := noMatchError("sess", "want", []probe{{socket: "sock-a", gotID: "id-a"}, {socket: "sock-b", gotID: "id-b"}}, nil, nil)
	zh := err.Error()
	if !strings.Contains(zh, "；") || !strings.Contains(zh, "身份") {
		t.Fatalf("中文身份说明应用分号：%q", zh)
	}
	code, args, ok := i18n.Wire(err)
	if !ok {
		t.Fatal("应有码")
	}
	i18n.SetLang(i18n.EN)
	en := deckclient.Text(code, args, zh)
	if strings.Contains(en, "身份") || strings.Contains(en, "；") || !strings.Contains(en, "; ") || !strings.Contains(en, "identity of the one on sock-a is id-a") {
		t.Fatalf("英文身份说明不应夹中文：%q", en)
	}
}

func TestN060LaunchEmptyReasonNestsCode(t *testing.T) {
	i18n.SetLang(i18n.ZH)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	dir := t.TempDir()
	bin := filepath.Join(dir, "agent-deck")
	script := "#!/bin/sh\nprintf '%s\\n' '{\"success\":false}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(bin, "deck", "", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Launch(context.Background(), LaunchRequest{ProjectPath: "/tmp/p", Tool: "claude", Model: "claude-opus-5-5", NoWait: true})
	if err == nil || !strings.Contains(err.Error(), "上游没有说明原因") {
		t.Fatalf("空原因的中文应变：%v", err)
	}
	code, args, ok := i18n.Wire(err)
	if !ok || code != "err.agentdeck.clientlaunch.2" {
		t.Fatalf("外层码不对：%v %s", ok, code)
	}
	i18n.SetLang(i18n.EN)
	en := deckclient.Text(code, args, err.Error())
	if strings.Contains(en, "上游") || !strings.Contains(en, "upstream gave no reason") {
		t.Fatalf("英文应展开嵌套码：%q", en)
	}

	rawBin := filepath.Join(dir, "agent-deck-raw")
	raw := "#!/bin/sh\nprintf '%s\\n' '{\"success\":false,\"error\":\"RAW-EXTERNAL\"}'\n"
	if err := os.WriteFile(rawBin, []byte(raw), 0o755); err != nil {
		t.Fatal(err)
	}
	c.Binary = rawBin
	_, err = c.Launch(context.Background(), LaunchRequest{ProjectPath: "/tmp/p", Tool: "claude", Model: "claude-opus-5-5", NoWait: true})
	if err == nil || !strings.Contains(err.Error(), "RAW-EXTERNAL") {
		t.Fatalf("上游原文应留在中文里：%v", err)
	}
	code, args, ok = i18n.Wire(err)
	en = deckclient.Text(code, args, err.Error())
	if !ok || !strings.Contains(en, "RAW-EXTERNAL") || strings.Contains(en, "no reason") {
		t.Fatalf("上游原文应原样进英文：%q", en)
	}
}
