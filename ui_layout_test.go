package main

import (
	"bytes"
	"testing"

	"golang.org/x/net/html"
)

func TestNativeChatScrimSharesShellStackingContext(t *testing.T) {
	data, err := assets.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	document, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	scrim := htmlElementByID(document, "nativeChatScrim")
	if scrim == nil {
		t.Fatal("nativeChatScrim not found")
	}
	if scrim.Parent == nil || !htmlElementHasClass(scrim.Parent, "nativeChatShell") {
		t.Fatal("nativeChatScrim must be a direct child of nativeChatShell so it stays below the mobile side panels")
	}
}

func TestCpaMintControlsHaveClientHandlers(t *testing.T) {
	htmlData, err := assets.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	appData, err := assets.ReadFile("ui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"startCpaMintBtn", "cancelCpaMintBtn", "openCpaMintUrlBtn", "grokPoolAuthDir"} {
		if !bytes.Contains(htmlData, []byte(`id="`+id+`"`)) {
			t.Fatalf("%s control not found", id)
		}
		if !bytes.Contains(appData, []byte(`$("`+id+`")`)) {
			t.Fatalf("%s client handler not found", id)
		}
	}
	for _, endpoint := range []string{"/api/cpa-mint", "/api/grok-pool/import-dir", "/api/grok-pool/open-auth-dir"} {
		if !bytes.Contains(appData, []byte(endpoint)) {
			t.Fatalf("client endpoint %s not found", endpoint)
		}
	}
}

func TestDefaultReasoningEffortControl(t *testing.T) {
	htmlData, err := assets.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	appData, err := assets.ReadFile("ui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	document, err := html.Parse(bytes.NewReader(htmlData))
	if err != nil {
		t.Fatal(err)
	}
	control := htmlElementByID(document, "defaultReasoningEffort")
	if control == nil || control.Data != "select" {
		t.Fatal("defaultReasoningEffort select not found")
	}
	connectBlock := htmlElementByID(document, "connectBlock")
	if connectBlock == nil || !htmlElementContains(connectBlock, control) {
		t.Fatal("defaultReasoningEffort must be inside connectBlock")
	}
	var values, labels []string
	for child := control.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != html.ElementNode || child.Data != "option" {
			continue
		}
		for _, attribute := range child.Attr {
			if attribute.Key == "value" {
				values = append(values, attribute.Val)
			}
		}
		if child.FirstChild != nil {
			labels = append(labels, child.FirstChild.Data)
		}
	}
	want := []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
	wantLabels := []string{"禁用推理 (none)", "最小 (minimal)", "低 (low)", "中 (medium)", "高 (high)", "超高 (xhigh)", "最大 (max，仅部分模型；当前 Grok CLI 不支持)"}
	if len(values) != len(want) {
		t.Fatalf("defaultReasoningEffort options = %v, want %v", values, want)
	}
	for i := range want {
		if values[i] != want[i] || labels[i] != wantLabels[i] {
			t.Fatalf("defaultReasoningEffort options = %v (%v), want %v (%v)", values, labels, want, wantLabels)
		}
	}
	if !bytes.Contains(appData, []byte(`"max"`)) || !bytes.Contains(htmlData, []byte(`value="max"`)) {
		t.Fatal("reasoning effort options must include max")
	}
	if !bytes.Contains(appData, []byte(`$("defaultReasoningEffort")`)) {
		t.Fatal("defaultReasoningEffort client binding not found")
	}
	for _, id := range []string{"detectReasoningEffortsBtn", "reasoningEffortStatus"} {
		if !bytes.Contains(htmlData, []byte(`id="`+id+`"`)) {
			t.Fatalf("%s control not found", id)
		}
		if !bytes.Contains(appData, []byte(`$("`+id+`")`)) {
			t.Fatalf("%s client binding not found", id)
		}
	}
	if !bytes.Contains(appData, []byte("/api/models/reasoning-efforts")) {
		t.Fatal("reasoning effort discovery endpoint not found")
	}
	if !bytes.Contains(appData, []byte("上游接受请求，可能静默忽略")) {
		t.Fatal("accepted reasoning effort disclaimer not found")
	}
}

func TestRegistrarControlsHaveClientHandlers(t *testing.T) {
	htmlData, err := assets.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	appData, err := assets.ReadFile("ui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{
		"registrarForm", "registrarSteps", "registrarAdvanced", "registrarCloudflareEssentials",
		"registrarProxyUrl", "registrarCloudflareApiBase",
		"probeRegistrarBtn", "startRegistrarBtn", "stopRegistrarBtn", "registrarLog",
	} {
		if !bytes.Contains(htmlData, []byte(`id="`+id+`"`)) {
			t.Fatalf("%s control not found", id)
		}
	}
	for _, id := range []string{"registrarForm", "probeRegistrarBtn", "startRegistrarBtn", "stopRegistrarBtn", "registrarLog"} {
		if !bytes.Contains(appData, []byte(`$("`+id+`")`)) {
			t.Fatalf("%s client handler not found", id)
		}
	}
	if !bytes.Contains(appData, []byte(`config.email_provider || "cloudflare"`)) {
		t.Fatal("registrar UI default email provider is not cloudflare")
	}
	if !bytes.Contains(htmlData, []byte("填写两项")) {
		t.Fatal("registrar 3-step guide not found")
	}
	for _, endpoint := range []string{"/api/registrar", "/api/registrar/probe", "/api/registrar/start", "/api/registrar/stop", "/api/registrar/job"} {
		if !bytes.Contains(appData, []byte(endpoint)) {
			t.Fatalf("client endpoint %s not found", endpoint)
		}
	}
	if !bytes.Contains(appData, []byte("registrarFormDirty")) {
		t.Fatal("registrar form dirty-state guard not found")
	}
}

func TestSubscriptionProxyPageContract(t *testing.T) {
	htmlData, err := assets.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	appData, err := assets.ReadFile("ui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	styleData, err := assets.ReadFile("ui/style.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"navSubscriptionProxyBtn", "viewSubscriptionProxy", "subscriptionStartBtn", "subscriptionStopBtn", "subscriptionRestartBtn", "subscriptionAccounts", "subscriptionModels", "saveSubscriptionModelsBtn", "runSubscriptionDiagnosticsBtn", "backFromSubscriptionProxyBtn"} {
		if !bytes.Contains(htmlData, []byte(`id="`+id+`"`)) {
			t.Fatalf("%s control not found", id)
		}
		if !bytes.Contains(appData, []byte(`$("`+id+`")`)) {
			t.Fatalf("%s client handler not found", id)
		}
	}
	for _, requestPart := range []string{
		`api("/api/subscription-proxy/service", { method: "POST", body: JSON.stringify({ action }) })`,
		`for (const action of ["start", "stop", "restart"])`,
		`headers: { "Content-Type": "application/json" }`,
	} {
		if !bytes.Contains(appData, []byte(requestPart)) {
			t.Fatalf("subscription service JSON request contract not found: %s", requestPart)
		}
	}
	for _, endpoint := range []string{"/api/subscription-proxy", "/api/subscription-proxy/service", "/api/subscription-proxy/login", "/api/subscription-proxy/login/open", "/api/subscription-proxy/accounts/", "/api/subscription-proxy/models", "/api/subscription-proxy/providers", "/api/subscription-proxy/diagnostics"} {
		if !bytes.Contains(appData, []byte(endpoint)) {
			t.Fatalf("client endpoint %s not found", endpoint)
		}
	}
	for _, marker := range []string{"subscriptionLoginGrid", "subscriptionProviderGrid", "@media (max-width: 375px)"} {
		if !bytes.Contains(styleData, []byte(marker)) {
			t.Fatalf("responsive class %s not found", marker)
		}
	}
}

func htmlElementByID(node *html.Node, id string) *html.Node {
	if node.Type == html.ElementNode {
		for _, attribute := range node.Attr {
			if attribute.Key == "id" && attribute.Val == id {
				return node
			}
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if found := htmlElementByID(child, id); found != nil {
			return found
		}
	}
	return nil
}

func htmlElementContains(root, target *html.Node) bool {
	if root == nil || target == nil {
		return false
	}
	if root == target {
		return true
	}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if htmlElementContains(child, target) {
			return true
		}
	}
	return false
}

func htmlElementHasClass(node *html.Node, className string) bool {
	if node == nil || node.Type != html.ElementNode {
		return false
	}
	for _, attribute := range node.Attr {
		if attribute.Key != "class" {
			continue
		}
		for _, current := range bytes.Fields([]byte(attribute.Val)) {
			if string(current) == className {
				return true
			}
		}
	}
	return false
}
