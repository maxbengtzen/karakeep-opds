package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

// Article images are fetched from the internet. So that nobody can use the service to reach
// internal addresses (SSRF), the IP address is checked when the connection is opened, that is
// after the DNS lookup. The original resolved the name first and connected afterwards, which
// leaves a gap (DNS rebinding).

const (
	maxRemoteImageBytes = 2_000_000
	maxRedirects        = 3
)

var errUnsafeAddress = errors.New("images: address is not public")

var blockedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// publicIP corresponds to safe_remote_ip, with a few more internal networks (among them CGNAT
// and addresses that map into IPv4 via IPv6).
func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() ||
		ip.IsUnspecified() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// newImageClient returns a client that only connects to public addresses and does not follow
// redirects by itself.
func newImageClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || !publicIP(ap.Addr()) {
				return errUnsafeAddress
			}
			return nil
		},
	}
	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// fetchRemoteImage corresponds to fetch_remote_image: only http(s), at most three redirects,
// only GIF, JPEG and PNG, at most 2 MB. Errors return nil.
func fetchRemoteImage(ctx context.Context, client *http.Client, rawURL string) *sourceImage {
	current := rawURL
	for range maxRedirects + 1 {
		u, err := url.Parse(current)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return nil
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, current, nil)
		if err != nil {
			return nil
		}
		req.Header.Set("User-Agent", "karakeep-opds/"+version)
		resp, err := client.Do(req)
		if err != nil {
			return nil
		}
		switch resp.StatusCode {
		case 301, 302, 303, 307, 308:
			resp.Body.Close()
			loc, err := resp.Location()
			if err != nil {
				return nil
			}
			current = loc.String()
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return nil
		}
		mt := mediaType(resp.Header.Get("Content-Type"), "")
		if !supportedEPUBImage(mt) {
			return nil
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxRemoteImageBytes+1))
		if err != nil || len(data) > maxRemoteImageBytes {
			return nil
		}
		return &sourceImage{data: data, mediaType: mt, source: rawURL}
	}
	return nil
}
