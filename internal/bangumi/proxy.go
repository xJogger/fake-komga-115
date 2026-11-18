package bangumi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const SettingProxyURL = "bangumi_proxy_url"

var ErrInvalidProxyURL = errors.New("invalid proxy URL: must use http://, https://, socks5://, or socks5h:// with host:port")

type EndpointProbe struct {
	OK        bool   `json:"ok"`
	LatencyMs int64  `json:"latencyMs"`
	Detail    string `json:"detail"`
	Error     string `json:"error,omitempty"`
}

type ConnectivityReport struct {
	ProxyURL string        `json:"proxyUrl"`
	Direct   bool          `json:"direct"`
	API      EndpointProbe `json:"api"`
	Archive  EndpointProbe `json:"archive"`
}

func ValidateProxyURL(raw string) (*url.URL, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, "", ErrInvalidProxyURL
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, "", ErrInvalidProxyURL
	}
	if u.Hostname() == "" {
		return nil, "", ErrInvalidProxyURL
	}
	return u, u.String(), nil
}

func RedactProxyURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Redacted()
}

func MergeProxyURLUpdate(inputRaw, existingRaw string) (string, error) {
	inputRaw = strings.TrimSpace(inputRaw)
	if inputRaw == "" {
		return "", nil
	}
	inURL, normalized, err := ValidateProxyURL(inputRaw)
	if err != nil {
		return "", err
	}
	if inURL.User != nil {
		if pwd, hasPwd := inURL.User.Password(); hasPwd && pwd == "xxxxx" && strings.TrimSpace(existingRaw) != "" {
			if exURL, _, exErr := ValidateProxyURL(existingRaw); exErr == nil && exURL != nil && exURL.User != nil {
				if exPwd, exHas := exURL.User.Password(); exHas &&
					exURL.User.Username() == inURL.User.Username() &&
					strings.EqualFold(exURL.Host, inURL.Host) {
					inURL.User = url.UserPassword(inURL.User.Username(), exPwd)
					return inURL.String(), nil
				}
			}
		}
	}
	return normalized, nil
}

func NewBangumiTransport(proxyURL string, responseHeaderTimeout time.Duration) (*http.Transport, error) {
	parsedProxy, _, err := ValidateProxyURL(proxyURL)
	if err != nil {
		return nil, err
	}
	var base *http.Transport
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		base = dt.Clone()
	} else {
		base = &http.Transport{}
	}
	base.DialContext = (&net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext
	base.TLSHandshakeTimeout = 15 * time.Second
	base.ResponseHeaderTimeout = responseHeaderTimeout
	base.IdleConnTimeout = 90 * time.Second
	if parsedProxy != nil {
		base.Proxy = http.ProxyURL(parsedProxy)
	} else {
		base.Proxy = nil
	}
	return base, nil
}

func IsAllowedBangumiCoverURL(rawURL string) bool {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && scheme != "http" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "127.0.0.1" || host == "localhost" {
		// Allow local httptest servers only when loopback HTTP is used in tests;
		// production Bangumi cover URLs are https on bgm.tv / lain.bgm.tv.
		return scheme == "http"
	}
	if scheme != "https" {
		return false
	}
	return host == "bgm.tv" ||
		host == "bangumi.tv" ||
		host == "chii.in" ||
		strings.HasSuffix(host, ".bgm.tv") ||
		strings.HasSuffix(host, ".bangumi.tv") ||
		strings.HasSuffix(host, ".chii.in")
}

func SanitizeProxyError(err error, proxyRaw string) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if strings.TrimSpace(proxyRaw) != "" {
		if u, pErr := url.Parse(proxyRaw); pErr == nil && u.User != nil {
			if pwd, ok := u.User.Password(); ok && pwd != "" {
				msg = strings.ReplaceAll(msg, pwd, "xxxxx")
			}
		}
	}
	return msg
}

func ProbeConnectivity(ctx context.Context, proxyRaw, apiBaseURL, archiveMetaURL string) (ConnectivityReport, error) {
	_, normalized, err := ValidateProxyURL(proxyRaw)
	if err != nil {
		return ConnectivityReport{}, err
	}
	tr, err := NewBangumiTransport(normalized, 12*time.Second)
	if err != nil {
		return ConnectivityReport{}, err
	}
	defer tr.CloseIdleConnections()

	client := &http.Client{
		Transport: tr,
		Timeout:   15 * time.Second,
	}
	report := ConnectivityReport{
		ProxyURL: RedactProxyURL(normalized),
		Direct:   normalized == "",
	}

	// 1. Probe Bangumi API
	if strings.TrimSpace(apiBaseURL) == "" {
		apiBaseURL = "https://api.bgm.tv"
	}
	apiStart := time.Now()
	apiReq, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(apiBaseURL, "/")+"/v0/subjects/1", nil)
	if err == nil {
		apiReq.Header.Set("User-Agent", archiveUserAgent)
		apiReq.Header.Set("Accept", "application/json")
		resp, doErr := client.Do(apiReq)
		report.API.LatencyMs = time.Since(apiStart).Milliseconds()
		if doErr != nil {
			report.API.Error = SanitizeProxyError(doErr, normalized)
		} else {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				report.API.OK = true
				report.API.Detail = fmt.Sprintf("HTTP %d", resp.StatusCode)
			} else {
				report.API.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
			}
		}
	}

	// 2. Probe Bangumi Archive metadata (GitHub)
	if strings.TrimSpace(archiveMetaURL) == "" {
		archiveMetaURL = DefaultArchiveMetaURL
	}
	archStart := time.Now()
	archReq, err := http.NewRequestWithContext(ctx, http.MethodGet, archiveMetaURL, nil)
	if err == nil {
		archReq.Header.Set("User-Agent", archiveUserAgent)
		resp, doErr := client.Do(archReq)
		report.Archive.LatencyMs = time.Since(archStart).Milliseconds()
		if doErr != nil {
			report.Archive.Error = SanitizeProxyError(doErr, normalized)
		} else {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var meta latestDumpMeta
				_ = json.Unmarshal(body, &meta)
				report.Archive.OK = true
				if meta.Name != "" {
					report.Archive.Detail = meta.Name
				} else {
					report.Archive.Detail = "HTTP 200"
				}
			} else {
				report.Archive.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
			}
		}
	}

	return report, nil
}
