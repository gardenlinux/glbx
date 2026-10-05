package container

import (
	"fmt"
	"os"
)

// readNSInode returns the namespace inode for a process and namespace type
// (e.g. "mnt", "user", "pid"), parsed from /proc/<pid>/ns/<type>. It returns
// "?" on failure. Used only for debug logging.
func readNSInode(pid int, nsType string) string {
	link, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/%s", pid, nsType))
	if err != nil {
		return "?"
	}
	// link is like "mnt:[4026532xxx]"
	start := len(nsType) + 2 // skip "<type>:["
	end := len(link) - 1     // skip trailing "]"
	if start < end && end <= len(link) {
		return link[start:end]
	}
	return link
}
