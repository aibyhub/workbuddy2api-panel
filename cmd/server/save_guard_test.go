// save_guard_test.go 面板保存配置的 api_key 非空闸：清空 = 网关与面板同时热生效
// 为「无鉴权」，保存路径必须拒绝。
package main

import (
	"strings"
	"testing"
)

func TestValidateSaveAPIKey(t *testing.T) {
	if err := validateSaveAPIKey(&Config{APIKey: "sk-abc"}); err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}
	if err := validateSaveAPIKey(&Config{APIKey: "  sk-abc  "}); err != nil {
		t.Fatalf("padded key rejected: %v", err)
	}
	for _, bad := range []string{"", "   "} {
		err := validateSaveAPIKey(&Config{APIKey: bad})
		if err == nil {
			t.Fatalf("empty api_key (%q) must be rejected", bad)
		}
		if !strings.Contains(err.Error(), "api_key 不能为空") {
			t.Fatalf("unexpected error text: %v", err)
		}
	}
}
