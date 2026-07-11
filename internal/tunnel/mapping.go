package tunnel

import (
	"fmt"
	"strconv"
	"strings"
)

type PortMapping struct {
	ListenAddr string
	TargetAddr string
}

func ParseMappings(items []string) ([]PortMapping, error) {
	res := make([]PortMapping, 0, len(items))
	for _, raw := range items {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}

		if strings.Contains(raw, ":") && strings.Contains(raw, "-") && !strings.Contains(raw, "=") {
			parts := strings.SplitN(raw, ":", 2)
			start, end, err := parseRange(strings.TrimSpace(parts[0]))
			if err != nil {
				return nil, err
			}
			tAddr, err := normalizeTarget(strings.TrimSpace(parts[1]))
			if err != nil {
				return nil, err
			}
			for p := start; p <= end; p++ {
				res = append(res, PortMapping{
					ListenAddr: fmt.Sprintf("0.0.0.0:%d", p),
					TargetAddr: tAddr,
				})
			}
			continue
		}

		if strings.Contains(raw, "=") {
			parts := strings.SplitN(raw, "=", 2)
			listenPart := strings.TrimSpace(parts[0])
			targetPart := strings.TrimSpace(parts[1])

			if strings.Contains(listenPart, "-") {
				start, end, err := parseRange(listenPart)
				if err != nil {
					return nil, err
				}
				tPort, err := parsePortMaybeWithHost(targetPart)
				if err != nil {
					return nil, err
				}
				for p := start; p <= end; p++ {
					res = append(res, PortMapping{
						ListenAddr: fmt.Sprintf("0.0.0.0:%d", p),
						TargetAddr: fmt.Sprintf("127.0.0.1:%d", tPort),
					})
				}
				continue
			}

			lPort, err := parsePortMaybeWithHost(listenPart)
			if err != nil {
				return nil, err
			}
			tAddr, err := normalizeTarget(targetPart)
			if err != nil {
				return nil, err
			}
			res = append(res, PortMapping{
				ListenAddr: fmt.Sprintf("0.0.0.0:%d", lPort),
				TargetAddr: tAddr,
			})
			continue
		}

		if strings.Contains(raw, "-") {
			start, end, err := parseRange(raw)
			if err != nil {
				return nil, err
			}
			for p := start; p <= end; p++ {
				res = append(res, PortMapping{
					ListenAddr: fmt.Sprintf("0.0.0.0:%d", p),
					TargetAddr: fmt.Sprintf("127.0.0.1:%d", p),
				})
			}
			continue
		}

		p, err := parsePortMaybeWithHost(raw)
		if err != nil {
			return nil, err
		}
		res = append(res, PortMapping{
			ListenAddr: fmt.Sprintf("0.0.0.0:%d", p),
			TargetAddr: fmt.Sprintf("127.0.0.1:%d", p),
		})
	}
	if len(res) == 0 {
		return nil, fmt.Errorf("no valid [ports].mapping entries found")
	}
	return res, nil
}

func parseRange(s string) (int, int, error) {
	parts := strings.SplitN(strings.TrimSpace(s), "-", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid range: %q", s)
	}
	start, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid range start: %q", s)
	}
	end, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid range end: %q", s)
	}
	if start < 1 || end > 65535 || start > end {
		return 0, 0, fmt.Errorf("range out of bounds: %q", s)
	}
	return start, end, nil
}

func parsePortMaybeWithHost(s string) (int, error) {
	s = strings.TrimSpace(s)
	if idx := strings.LastIndex(s, ":"); idx >= 0 {
		s = s[idx+1:]
	}
	p, err := strconv.Atoi(s)
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("invalid port: %q", s)
	}
	return p, nil
}

func normalizeTarget(s string) (string, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, ":") {
		_, err := parsePortMaybeWithHost(s)
		if err != nil {
			return "", err
		}
		return s, nil
	}
	p, err := parsePortMaybeWithHost(s)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("127.0.0.1:%d", p), nil
}
