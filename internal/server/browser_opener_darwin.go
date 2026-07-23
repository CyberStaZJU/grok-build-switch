//go:build darwin

package server

import (
	"fmt"
	"net/url"
	"os/exec"
)

type SafeBrowserOpener struct{}

func (SafeBrowserOpener) Open(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return fmt.Errorf("登录地址无效")
	}
	if err := exec.Command("/usr/bin/open", raw).Start(); err != nil {
		return fmt.Errorf("无法打开登录页面")
	}
	return nil
}
