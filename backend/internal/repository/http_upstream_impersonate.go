package repository

import (
	"fmt"
	"io"
	"net/http"
)

// 编译期保证：httpUpstreamService 的 DoImpersonateChrome 签名与 service 层
// chromeImpersonatingUpstream 接口一致；否则 service 的类型断言会静默回退到普通 Do。
var _ interface {
	DoImpersonateChrome(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error)
} = (*httpUpstreamService)(nil)

// DoImpersonateChrome 通过共享的 Chrome 指纹伪装客户端（imroc/req：JA3/JA4 TLS + HTTP/2 指纹）
// 发送一个标准库 *http.Request，返回内嵌的流式 *http.Response（调用方负责 Close Body）。
//
// 用途：针对需要绕过 Cloudflare bot 检测的上游（如 chatgpt.com/backend-api/codex/responses）。
// 它复用 req_client_pool 的共享客户端池（按代理缓存、复用连接），并复用本服务的 SSRF 主机校验。
// 注意：该路径走 req 自己的连接池，不参与本 service 的 *http.Transport 池/inFlight 统计，
// 但生图的并发由 handler 层的 account slot 控制，因此不影响调度正确性。
func (s *httpUpstreamService) DoImpersonateChrome(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("impersonate chrome: nil request")
	}
	if err := s.validateRequestHost(req); err != nil {
		return nil, err
	}

	client, err := getSharedReqClient(reqClientOptions{
		ProxyURL:    proxyURL,
		Timeout:     0, // 流式（SSE）请求不设总超时
		Impersonate: true,
	})
	if err != nil {
		return nil, err
	}

	var bodyBytes []byte
	if req.Body != nil {
		bodyBytes, err = io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
	}

	// 用调用方已构造好的全套头部（Codex 鉴权/会话头等）覆盖 ImpersonateChrome 的默认头，
	// 从而保持「应用层 Codex 身份 + 传输层 Chrome 指纹」。
	r := client.R().
		SetContext(req.Context()).
		DisableAutoReadResponse().
		SetHeaders(httpHeaderFirstValues(req.Header))
	if len(bodyBytes) > 0 {
		r = r.SetBodyBytes(bodyBytes)
	}

	reqResp, err := r.Send(req.Method, req.URL.String())
	if err != nil {
		return nil, err
	}
	if reqResp == nil || reqResp.Response == nil {
		return nil, fmt.Errorf("impersonate chrome: nil response")
	}
	return reqResp.Response, nil
}

// httpHeaderFirstValues 将 http.Header 转成 req 需要的 map[string]string（取首值）。
func httpHeaderFirstValues(h http.Header) map[string]string {
	m := make(map[string]string, len(h))
	for k, vs := range h {
		if len(vs) > 0 {
			m[k] = vs[0]
		}
	}
	return m
}
