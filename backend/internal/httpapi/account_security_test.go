package httpapi

import (
	"strings"
	"testing"
)

func TestSecurityMetadataIsBoundedAndDoesNotClaimGeolocation(t *testing.T) {
	if len([]rune(securityAgent(strings.Repeat("界", 700)))) != 512 {
		t.Fatal("user agent not bounded")
	}
	for ip, label := range map[string]string{"127.0.0.1": "本机或代理入口", "192.168.1.2": "内网来源", "2001:4860:4860::8888": "公网来源", "": "来源未知"} {
		if sourceLabel(ip) != label {
			t.Fatalf("source label for %q", ip)
		}
	}
	if deviceLabel("Mozilla/5.0 (Windows NT 10.0) Chrome/130.0 Edg/130.0") != "Windows · Edge" {
		t.Fatal("device classification")
	}
}
