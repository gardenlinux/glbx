package container

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
)

type IDRange struct {
	Start uint32
	Count uint32
}

type IDMapping struct {
	Inner uint32
	Outer uint32
	Count uint32
}

func GetSubordinateRanges(uid bool) ([]IDRange, error) {
	u, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("get current user: %w", err)
	}

	flag := "-u"
	if !uid {
		flag = "-g"
	}

	cmd := exec.Command("getsubids", flag, u.Username)
	out, err := cmd.Output()
	if err != nil {
		return parseSubIDFile(u, uid)
	}

	return parseGetSubidsOutput(out)
}

func parseGetSubidsOutput(out []byte) ([]IDRange, error) {
	var ranges []IDRange
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		for _, p := range parts {
			if strings.Contains(p, "-") {
				dashParts := strings.SplitN(p, "-", 2)
				if len(dashParts) == 2 {
					start, err1 := strconv.ParseUint(dashParts[0], 10, 32)
					end, err2 := strconv.ParseUint(dashParts[1], 10, 32)
					if err1 == nil && err2 == nil && end >= start {
						ranges = append(ranges, IDRange{Start: uint32(start), Count: uint32(end - start + 1)})
					}
				}
			}
		}
		if len(ranges) == 0 {
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				start, err1 := strconv.ParseUint(fields[len(fields)-2], 10, 32)
				count, err2 := strconv.ParseUint(fields[len(fields)-1], 10, 32)
				if err1 == nil && err2 == nil {
					ranges = append(ranges, IDRange{Start: uint32(start), Count: uint32(count)})
				}
			}
		}
	}
	return ranges, nil
}

func parseSubIDFile(u *user.User, uid bool) ([]IDRange, error) {
	path := "/etc/subuid"
	if !uid {
		path = "/etc/subgid"
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var ranges []IDRange
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		if parts[0] != u.Username && parts[0] != u.Uid {
			continue
		}
		start, err1 := strconv.ParseUint(parts[1], 10, 32)
		count, err2 := strconv.ParseUint(parts[2], 10, 32)
		if err1 != nil || err2 != nil {
			continue
		}
		ranges = append(ranges, IDRange{Start: uint32(start), Count: uint32(count)})
	}
	return ranges, scanner.Err()
}

func ReadProcIDMap(pid int, uid bool) ([]IDMapping, error) {
	name := "uid_map"
	if !uid {
		name = "gid_map"
	}
	path := fmt.Sprintf("/proc/%d/%s", pid, name)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var mappings []IDMapping
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		inner, _ := strconv.ParseUint(fields[0], 10, 32)
		outer, _ := strconv.ParseUint(fields[1], 10, 32)
		count, _ := strconv.ParseUint(fields[2], 10, 32)
		mappings = append(mappings, IDMapping{
			Inner: uint32(inner),
			Outer: uint32(outer),
			Count: uint32(count),
		})
	}
	return mappings, nil
}

func ComputeIDMappings(subRanges []IDRange, parentMap []IDMapping, requestedCount uint32) ([]IDMapping, error) {
	var usable []IDRange
	for _, sub := range subRanges {
		for _, pm := range parentMap {
			intStart := max32(sub.Start, pm.Outer)
			intEnd := min32(sub.Start+sub.Count, pm.Outer+pm.Count)
			if intStart < intEnd {
				usable = append(usable, IDRange{Start: intStart, Count: intEnd - intStart})
			}
		}
	}

	if len(usable) == 0 && len(parentMap) > 0 {
		for _, sub := range subRanges {
			usable = append(usable, IDRange{Start: sub.Start, Count: sub.Count})
		}
	}

	var mappings []IDMapping
	var allocated uint32
	for _, r := range usable {
		if allocated >= requestedCount {
			break
		}
		need := requestedCount - allocated
		take := r.Count
		if take > need {
			take = need
		}
		mappings = append(mappings, IDMapping{
			Inner: allocated,
			Outer: r.Start,
			Count: take,
		})
		allocated += take
	}

	if allocated < requestedCount {
		return nil, fmt.Errorf("insufficient subordinate IDs: need %d, have %d", requestedCount, allocated)
	}

	return mappings, nil
}

func ApplyIDMappings(pid int, uidMappings, gidMappings []IDMapping) error {
	if err := applyMapping("newuidmap", pid, uidMappings); err != nil {
		return fmt.Errorf("apply uid map: %w", err)
	}
	if err := applyMapping("newgidmap", pid, gidMappings); err != nil {
		return fmt.Errorf("apply gid map: %w", err)
	}
	return nil
}

func applyMapping(tool string, pid int, mappings []IDMapping) error {
	args := []string{strconv.Itoa(pid)}
	for _, m := range mappings {
		args = append(args, strconv.FormatUint(uint64(m.Inner), 10))
		args = append(args, strconv.FormatUint(uint64(m.Outer), 10))
		args = append(args, strconv.FormatUint(uint64(m.Count), 10))
	}
	cmd := exec.Command(tool, args...)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func max32(a, b uint32) uint32 {
	if a > b {
		return a
	}
	return b
}

func min32(a, b uint32) uint32 {
	if a < b {
		return a
	}
	return b
}
